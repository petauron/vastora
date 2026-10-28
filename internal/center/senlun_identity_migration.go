package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/senulun"
)

const senlunNativeIdentityKey = "vastora-official/senlun-agent"
const legacySenlunNativeIdentityKey = "vastora-official/pulse-agent"

func senlunNativeIdentityRequest(request DeploymentRequest) bool {
	return request.AppKey == senlunNativeIdentityKey && request.ReplaceApplicationID != "" && request.SourceReceiptSHA256 != "" && request.Operation == "install"
}

// Check both the signed historical deployment and the current Agent receipt.
// A changed artifact, permission or runtime is not an identity-only migration.
func validateSenlunNativeIdentitySource(ctx context.Context, tx *sql.Tx, applicationID, expectedSHA string, target catalog.AppManifest, allowPending bool) (string, error) {
	var nodeID, status, adoption, version string
	var rawReceipt, rawManifest []byte
	var revision int
	err := tx.QueryRowContext(ctx, `SELECT a.node_id,a.status,r.adoption_state,r.resources_json,d.manifest_json,d.app_version,d.package_revision
		FROM applications a JOIN application_resources r ON r.application_id=a.id
		JOIN deployments d ON d.rowid=(SELECT old.rowid FROM deployments old WHERE old.application_id=a.id AND old.app_key=a.app_key AND old.state='succeeded' ORDER BY old.created_at DESC,old.rowid DESC LIMIT 1)
		WHERE a.id=? AND a.app_key=? AND d.operation IN ('install','upgrade','configure')`, applicationID, legacySenlunNativeIdentityKey).Scan(&nodeID, &status, &adoption, &rawReceipt, &rawManifest, &version, &revision)
	if err != nil || (allowPending && status != "pending") || (!allowPending && (status == "pending" || status == "deploying")) || adoption != "ready" {
		return "", errors.New("center: original Senlun ownership requires review")
	}
	var receipt struct {
		Version         int    `json:"version"`
		ApplicationID   string `json:"applicationId"`
		AppKey          string `json:"appKey"`
		Runtime         string `json:"runtime"`
		State           string `json:"state"`
		PackageVersion  string `json:"packageVersion"`
		PackageRevision int    `json:"packageRevision"`
		ManifestSHA256  string `json:"manifestSha256"`
	}
	digest := sha256.Sum256(rawReceipt)
	if len(rawReceipt) > 1<<20 || hex.EncodeToString(digest[:]) != expectedSHA || json.Unmarshal(rawReceipt, &receipt) != nil || receipt.Version != 1 || receipt.ApplicationID != applicationID || receipt.AppKey != legacySenlunNativeIdentityKey || receipt.Runtime != "systemd" || receipt.State != "ready" || receipt.PackageVersion != version || receipt.PackageRevision != revision {
		return "", errors.New("center: original Senlun resource receipt differs")
	}
	var previous catalog.AppManifest
	if json.Unmarshal(rawManifest, &previous) != nil || previous.Version != version || previous.PackageRevision != revision {
		return "", errors.New("center: original Senlun recipe is invalid")
	}
	_, _, originalSHA, err := canonicalPackage(previous)
	if err != nil || originalSHA != receipt.ManifestSHA256 {
		return "", errors.New("center: original Senlun recipe does not match its receipt")
	}
	if err := senulun.ValidateIdentityRename(previous, target); err != nil {
		return "", err
	}
	return nodeID, nil
}

func (s *Store) prepareSenlunNativeIdentity(ctx context.Context, tx *sql.Tx, request DeploymentRequest, target catalog.AppManifest, now time.Time) (string, error) {
	nodeID, err := validateSenlunNativeIdentitySource(ctx, tx, request.ReplaceApplicationID, request.SourceReceiptSHA256, target, false)
	if err != nil || nodeID != request.AgentID {
		return "", errors.New("center: Senlun identity source or target node differs")
	}
	if err := requireNodeRuntime(ctx, tx, nodeID, target); err != nil {
		return "", err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM deployments WHERE application_id=? AND (state IN ('pending','running') OR reconciliation_required=1))+(SELECT COUNT(*) FROM application_commands WHERE application_id=? AND state IN ('pending','running'))+(SELECT COUNT(*) FROM applications WHERE node_id=? AND app_key=? AND id<>?)`, request.ReplaceApplicationID, request.ReplaceApplicationID, nodeID, senlunNativeIdentityKey, request.ReplaceApplicationID).Scan(&active); err != nil {
		return "", err
	}
	if active != 0 {
		return "", errors.New("center: resolve existing operations or target identity conflicts before migration")
	}
	if err := retainNativeReplacementBindings(ctx, tx, request.ReplaceApplicationID, target); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE applications SET status='pending',updated_at=? WHERE id=?`, now.Format(time.RFC3339Nano), request.ReplaceApplicationID); err != nil {
		return "", err
	}
	return request.ReplaceApplicationID, nil
}

func completeSenlunNativeIdentity(ctx context.Context, tx *sql.Tx, applicationID, expectedSHA string, targetRaw []byte, now time.Time) error {
	var target catalog.AppManifest
	if json.Unmarshal(targetRaw, &target) != nil || target.ID != "senlun-agent" {
		return errors.New("center: authorized Senlun target recipe is invalid")
	}
	// prepare marked this same instance pending. Recheck the original receipt
	// and signed recipe in the same transaction as the identity update.
	if _, err := validateSenlunNativeIdentitySource(ctx, tx, applicationID, expectedSHA, target, true); err != nil {
		return err
	}
	if err := retainNativeReplacementBindings(ctx, tx, applicationID, target); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE applications SET app_key=?,updated_at=? WHERE id=? AND app_key=?`, senlunNativeIdentityKey, now.Format(time.RFC3339Nano), applicationID, legacySenlunNativeIdentityKey)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return errors.New("center: Senlun application identity changed during migration")
	}
	return nil
}
