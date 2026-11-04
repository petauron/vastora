package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/petauron/vastora/internal/meridianruntime"
)

// Recover only an observed native entry's superseded package failure. Native
// package results do not report these separately registered public services.
// Publication verification still performs its normal DNS and reachability checks.
func (s *Store) reconcileNativeEntryServiceHealth(ctx context.Context, publicationID string, revision int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var applicationID, agentID, serviceID, endpoint, previousError, serviceUpdated, seen, taskID, version, digest, created string
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT a.id,a.node_id,service.id,service.endpoint,service.last_error,service.updated_at,
 node.last_seen_at,resources.resources_json,deployment.id,deployment.app_version,deployment.manifest_sha256,deployment.created_at
 FROM publications publication JOIN services service ON service.id=publication.service_id
 JOIN applications a ON a.id=service.application_id JOIN agents node ON node.id=a.node_id
 JOIN application_resources resources ON resources.application_id=a.id
 JOIN deployments deployment ON deployment.id=(SELECT id FROM deployments WHERE application_id=a.id
   AND state='succeeded' AND operation IN ('install','upgrade') ORDER BY created_at DESC LIMIT 1)
 WHERE publication.id=? AND publication.desired_revision=? AND publication.status<>'stopped' AND publication.kind='public_direct'
 AND a.app_key='vastora-official/glivela-entry' AND a.runtime='host' AND a.status='running'
 AND node.status='active' AND node.credential_revoked_at='' AND resources.adoption_state='ready'
 AND service.name='native-entry' AND service.source='observed' AND service.app_protocol=?
 AND service.status='degraded' AND service.last_error<>'' AND deployment.created_at>service.updated_at
 AND EXISTS(SELECT 1 FROM deployments failed JOIN task_executions execution ON execution.task_id=failed.id
   WHERE failed.application_id=a.id AND failed.agent_id=a.node_id AND failed.operation IN ('install','upgrade')
   AND failed.state='failed' AND execution.agent_id=a.node_id AND execution.kind='application.apply' AND execution.state='failed'
   AND execution.last_error=service.last_error AND execution.updated_at<deployment.created_at)`, publicationID, revision, meridianEntryProtocol).
		Scan(&applicationID, &agentID, &serviceID, &endpoint, &previousError, &serviceUpdated, &seen, &raw, &taskID, &version, &digest, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var receipt struct {
		ApplicationID  string `json:"applicationId"`
		AppKey         string `json:"appKey"`
		TaskID         string `json:"taskId"`
		PackageVersion string `json:"packageVersion"`
		ManifestSHA256 string `json:"manifestSha256"`
		Runtime        string `json:"runtime"`
		State          string `json:"state"`
	}
	now := s.now().UTC()
	seenAt, seenErr := time.Parse(time.RFC3339Nano, seen)
	createdAt, createdErr := time.Parse(time.RFC3339Nano, created)
	if json.Unmarshal(raw, &receipt) != nil || receipt.ApplicationID != applicationID || receipt.AppKey != "vastora-official/glivela-entry" || receipt.TaskID != taskID || receipt.PackageVersion != version || digest == "" || receipt.ManifestSHA256 != digest || receipt.Runtime != "systemd" || receipt.State != "ready" || seenErr != nil || seenAt.After(now) || now.Sub(seenAt) > agentConnectedMaxAge || createdErr != nil || createdAt.After(now) {
		return errors.New("center: current native package recovery receipt required")
	}
	var active int
	err = tx.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM deployments WHERE application_id=? AND (state IN ('pending','running') OR reconciliation_required=1))+
 (SELECT COUNT(*) FROM application_commands WHERE application_id=? AND (state IN ('pending','running') OR reconciliation_required=1))+
 (SELECT COUNT(*) FROM application_adoptions WHERE application_id=? AND state IN ('pending','running'))+
 (SELECT COUNT(*) FROM application_maintenance WHERE application_id=? AND (state IN ('pending','running') OR reconciliation_required=1))`, applicationID, applicationID, applicationID, applicationID).Scan(&active)
	if err != nil {
		return err
	}
	if active != 0 {
		return errors.New("center: resolve active native application work before health verification")
	}
	path := s.glivelaPilotStatePath
	if path == "" {
		path = "/run/glivela-pilot/nodes.json"
	}
	evidence, err := meridianruntime.ReadNativeEntryRegistration(path, applicationID, now)
	if err != nil {
		return err
	}
	entry := evidence.Plan.RealityEndpoints[0]
	var hostname string
	if err := tx.QueryRowContext(ctx, `SELECT hostname FROM publications WHERE id=? AND service_id=? AND desired_revision=?`, publicationID, serviceID, revision).Scan(&hostname); err != nil {
		return err
	}
	if evidence.Receipt.StartedAt.Before(createdAt) || endpoint != net.JoinHostPort(entry.ListenAddress, strconv.Itoa(entry.ListenPort)) || hostname != entry.AdvertiseHost {
		return errors.New("center: current native runtime does not match the registered entry")
	}
	result, err := tx.ExecContext(ctx, `UPDATE services SET status='ready',last_error='',updated_at=?
 WHERE id=? AND application_id=? AND status='degraded' AND last_error=? AND updated_at=?`, now.Format(time.RFC3339Nano), serviceID, applicationID, previousError, serviceUpdated)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("center: native entry service changed during verification")
	}
	if err := s.recordTaskEvent(ctx, tx, serviceID, agentID, "glivela.native.health", int64(evidence.Plan.Revision), "succeeded", "Verified current native runtime after package recovery"); err != nil {
		return err
	}
	return tx.Commit()
}
