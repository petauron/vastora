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

// Sync only display metadata owned by the authenticated Glivela application.
// Endpoint/credential identities, listener routing and accounting are untouched.
func (s *Store) reconcileNativeEntryPublicationsInTx(ctx context.Context, tx *sql.Tx, applicationID string) ([]publicationVerificationTarget, error) {
	var agentID, endpointID, serviceID, endpoint, publicKey string
	var checkpointJSON []byte
	err := tx.QueryRowContext(ctx, `SELECT a.node_id,e.id,service.id,service.endpoint,e.public_key,checkpoint.checkpoint_json
 FROM applications a JOIN services service ON service.application_id=a.id
 JOIN meridian_endpoints e ON e.service_id=service.id
 JOIN glivela_pilot_checkpoint checkpoint ON checkpoint.endpoint_id=e.id
 WHERE a.id=? AND a.app_key='vastora-official/glivela-entry' AND a.runtime='host' AND a.status='running'
 AND service.name='native-entry' AND service.source='observed' AND service.status='ready' AND e.status<>'retired'
 AND NOT EXISTS (SELECT 1 FROM deployments d WHERE d.application_id=a.id AND (d.state IN ('pending','running') OR d.reconciliation_required=1))`, applicationID).
		Scan(&agentID, &endpointID, &serviceID, &endpoint, &publicKey, &checkpointJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("center: applied native entry metadata is not ready")
	}
	if err != nil {
		return nil, err
	}
	var checkpoint meridianruntime.PilotCounters
	if json.Unmarshal(checkpointJSON, &checkpoint) != nil || checkpoint.EntryID != applicationID {
		return nil, errors.New("center: native entry metadata identity unavailable")
	}
	path := s.glivelaPilotStatePath
	if path == "" {
		path = "/run/glivela-pilot/nodes.json"
	}
	evidence, err := meridianruntime.ReadAppliedNativeEntryEndpoint(path, applicationID, endpointID, checkpoint.CertificateID, s.now().UTC())
	if err != nil {
		return nil, err
	}
	if endpoint != net.JoinHostPort(evidence.ListenAddress, strconv.Itoa(evidence.ListenPort)) || publicKey != evidence.PublicKey {
		return nil, errors.New("center: native entry metadata differs from registered listener identity")
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,desired_revision,hostname,sni_hostname FROM publications WHERE service_id=? AND entry_node_id=? AND ingress_owner='application_node' AND kind='public_direct' AND status<>'stopped'`, serviceID, agentID)
	if err != nil {
		return nil, err
	}
	var targets []publicationVerificationTarget
	for rows.Next() {
		var target publicationVerificationTarget
		var hostname, sni string
		if err := rows.Scan(&target.id, &target.revision, &hostname, &sni); err != nil {
			rows.Close()
			return nil, err
		}
		if hostname != evidence.AdvertiseHost {
			rows.Close()
			return nil, errors.New("center: native entry hostname differs from registered publication")
		}
		if sni != evidence.ServerNames[0] {
			targets = append(targets, target)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	names, err := json.Marshal(evidence.ServerNames)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE meridian_endpoints SET server_names_json=? WHERE id=? AND application_id=? AND CAST(server_names_json AS TEXT)<>?`, names, endpointID, applicationID, string(names)); err != nil {
		return nil, err
	}
	for i := range targets {
		target := &targets[i]
		updated, err := tx.ExecContext(ctx, `UPDATE publications SET sni_hostname=?,desired_revision=desired_revision+1,status='pending',last_error='',updated_at=? WHERE id=? AND desired_revision=? AND status<>'stopped'`, evidence.ServerNames[0], stamp, target.id, target.revision)
		if err != nil {
			return nil, err
		}
		if count, err := updated.RowsAffected(); err != nil || count != 1 {
			return nil, errors.New("center: native entry publication changed during metadata sync")
		}
		target.revision++
	}
	return targets, nil
}

// Direct native entries have no gateway/listener task to schedule their check.
// Collect inside the successful result transaction, then schedule after commit.
func nativeEntryVerificationTargetsForDeployment(ctx context.Context, tx *sql.Tx, taskID string) ([]publicationVerificationTarget, error) {
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.desired_revision FROM deployments d
 JOIN applications a ON a.id=d.application_id JOIN services service ON service.application_id=a.id
 JOIN publications p ON p.service_id=service.id
 WHERE d.id=? AND d.state='succeeded' AND d.reconciliation_required=0 AND d.operation IN ('install','upgrade','configure')
 AND a.app_key='vastora-official/glivela-entry' AND a.runtime='host' AND a.status='running'
 AND service.name='native-entry' AND service.source='observed' AND service.status='ready'
 AND p.kind='public_direct' AND p.ingress_owner='application_node' AND p.entry_node_id=a.node_id AND p.status<>'stopped'`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []publicationVerificationTarget
	for rows.Next() {
		var target publicationVerificationTarget
		if err := rows.Scan(&target.id, &target.revision); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

// Reconcile the already registered card on access, using the application as
// the only SNI source. No legacy accounting loop or new polling is started.
// A missing/invalid source affects only its own cards, not the application list.
func (s *Store) syncNativeEntryPublicationMetadata(ctx context.Context, publicationID string) (map[string]error, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.id,a.id FROM publications p
 JOIN services service ON service.id=p.service_id JOIN applications a ON a.id=service.application_id
 JOIN meridian_endpoints e ON e.service_id=service.id JOIN glivela_pilot_checkpoint checkpoint ON checkpoint.endpoint_id=e.id
 WHERE (?='' OR p.id=?) AND a.app_key='vastora-official/glivela-entry' AND a.runtime='host'
 AND service.name='native-entry' AND service.source='observed' AND p.kind='public_direct'
 AND p.ingress_owner='application_node' AND p.entry_node_id=a.node_id AND p.status<>'stopped' AND e.status<>'retired'`, publicationID, publicationID)
	if err != nil {
		return nil, err
	}
	byApplication := map[string][]string{}
	for rows.Next() {
		var id, applicationID string
		if err := rows.Scan(&id, &applicationID); err != nil {
			rows.Close()
			return nil, err
		}
		byApplication[applicationID] = append(byApplication[applicationID], id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	unavailable := map[string]error{}
	for applicationID, ids := range byApplication {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		targets, syncErr := s.reconcileNativeEntryPublicationsInTx(ctx, tx, applicationID)
		if syncErr == nil {
			syncErr = tx.Commit()
		}
		tx.Rollback()
		if syncErr != nil {
			for _, id := range ids {
				unavailable[id] = errors.New("center: waiting for authenticated applied Glivela entry metadata")
			}
			continue
		}
		for _, target := range targets {
			s.schedulePublicationVerification(target.id, target.revision)
		}
	}
	return unavailable, nil
}

func nativeEntryMetadataUnavailable(value *PublicationView, err error) {
	if err == nil {
		return
	}
	value.SNIHostname = ""
	value.nativeMetadataError = err
	if value.LastError == "" {
		value.LastError = err.Error()
	}
	if value.Status == "ready" {
		value.Status = "degraded"
	}
}
