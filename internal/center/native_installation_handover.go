package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/petauron/catalog/catalog"
)

// Reuse normal installation authorization, delivery and completion. Only an
// explicit handover retains an existing identity instead of allocating a new
// application. The old catalog key is not changed until a matching real result.
func (s *Store) prepareNativeReplacement(ctx context.Context, tx *sql.Tx, request DeploymentRequest, manifest catalog.AppManifest, now time.Time) (string, error) {
	decoded, err := hex.DecodeString(request.SourceReceiptSHA256)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != request.SourceReceiptSHA256 || request.Operation != "install" || !request.BackupsConfirmed || request.AuthorizedCapabilities == nil || !strings.HasPrefix(request.AppKey, OfficialCatalogSourceID+"/") || manifest.Runtime == nil || manifest.Runtime.Kind != "systemd" || manifest.Runtime.Version != 2 {
		return "", errors.New("center: reviewed native installation handover required")
	}
	var sourceKey, sourceStatus, adoption string
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT a.app_key,a.status,r.adoption_state,r.resources_json FROM applications a JOIN application_resources r ON r.application_id=a.id WHERE a.id=? AND a.node_id=?`, request.ReplaceApplicationID, request.AgentID).Scan(&sourceKey, &sourceStatus, &adoption, &raw); err != nil {
		return "", errors.New("center: original application ownership was not found")
	}
	if sourceKey == request.AppKey || adoption != "ready" || sourceStatus == "pending" || sourceStatus == "deploying" || !sourceReceiptMatches(raw, request.ReplaceApplicationID, sourceKey, request.SourceReceiptSHA256) {
		return "", errors.New("center: original application receipt changed or requires review")
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM deployments WHERE application_id=? AND (state IN ('pending','running') OR reconciliation_required=1))+(SELECT COUNT(*) FROM application_commands WHERE application_id=? AND (state IN ('pending','running') OR reconciliation_required=1))+(SELECT COUNT(*) FROM applications WHERE node_id=? AND app_key=? AND id<>?)`, request.ReplaceApplicationID, request.ReplaceApplicationID, request.AgentID, request.AppKey, request.ReplaceApplicationID).Scan(&active); err != nil {
		return "", err
	}
	if active != 0 {
		return "", errors.New("center: resolve existing operations or target identity conflicts before handover")
	}
	if err := retainNativeReplacementBindings(ctx, tx, request.ReplaceApplicationID, manifest); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE applications SET status='pending',updated_at=? WHERE id=?`, now.Format(time.RFC3339Nano), request.ReplaceApplicationID); err != nil {
		return "", err
	}
	return request.ReplaceApplicationID, nil
}

func sourceReceiptMatches(raw []byte, applicationID, appKey, expected string) bool {
	digest := sha256.Sum256(raw)
	var receipt struct {
		Version       int    `json:"version"`
		ApplicationID string `json:"applicationId"`
		AppKey        string `json:"appKey"`
		Runtime       string `json:"runtime"`
		State         string `json:"state"`
	}
	return len(raw) <= 1<<20 && json.Unmarshal(raw, &receipt) == nil && receipt.Version == 1 && receipt.ApplicationID == applicationID && receipt.AppKey == appKey && receipt.Runtime == "docker" && receipt.State == "ready" && hex.EncodeToString(digest[:]) == expected
}

func retainNativeReplacementBindings(ctx context.Context, tx *sql.Tx, applicationID string, manifest catalog.AppManifest) error {
	declared := map[string]catalog.Service{}
	for _, service := range manifest.Services {
		declared[service.Name] = service
	}
	rows, err := tx.QueryContext(ctx, `SELECT name,protocol,proxy_protocol,management FROM services WHERE application_id=? AND status<>'stopped'`, applicationID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name, protocol, forwarding string
		var management bool
		if err := rows.Scan(&name, &protocol, &forwarding, &management); err != nil {
			return err
		}
		service, found := declared[name]
		if !found || service.Protocol != protocol || service.ProxyProtocol != forwarding || service.Management != management {
			return errors.New("center: handover must retain existing service names, forwarding and access boundaries")
		}
	}
	return rows.Err()
}

func completeNativeReplacement(ctx context.Context, tx *sql.Tx, taskID, applicationID string, result ApplicationTaskResult, now time.Time) error {
	var sourceSHA, targetKey, sourceKey string
	var manifestRaw []byte
	if err := tx.QueryRowContext(ctx, `SELECT d.source_receipt_sha256,d.app_key,a.app_key,d.manifest_json FROM deployments d JOIN applications a ON a.id=d.application_id WHERE d.id=? AND d.application_id=?`, taskID, applicationID).Scan(&sourceSHA, &targetKey, &sourceKey, &manifestRaw); err != nil {
		return err
	}
	if sourceSHA == "" {
		if result.SourceReceiptSHA256 != "" {
			return errors.New("center: unexpected installation handover evidence")
		}
		return nil
	}
	if sourceSHA != result.SourceReceiptSHA256 || sourceKey == targetKey {
		return errors.New("center: original installation handover evidence differs")
	}
	var previous []byte
	if err := tx.QueryRowContext(ctx, `SELECT resources_json FROM application_resources WHERE application_id=? AND adoption_state='ready'`, applicationID).Scan(&previous); err != nil || !sourceReceiptMatches(previous, applicationID, sourceKey, sourceSHA) {
		return errors.New("center: original resource registration changed during handover")
	}
	var manifest catalog.AppManifest
	if json.Unmarshal(manifestRaw, &manifest) != nil || manifest.Runtime == nil || manifest.Runtime.Kind != "systemd" || manifest.Runtime.Version != 2 {
		return errors.New("center: authorized native handover target missing")
	}
	if err := retainNativeReplacementBindings(ctx, tx, applicationID, manifest); err != nil {
		return err
	}
	// The caller validates the returned recipe/resources and service results in
	// this same transaction. Failure rolls back this key change as well. Service
	// and publication rows are updated by stable identity, never deleted/rebuilt.
	_, err := tx.ExecContext(ctx, `UPDATE applications SET app_key=?,role='',updated_at=? WHERE id=? AND app_key=?`, targetKey, now.Format(time.RFC3339Nano), applicationID, sourceKey)
	return err
}
