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
	"golang.org/x/mod/semver"
)

const senlunNativeIdentityKey = "vastora-official/senlun-agent"
const legacySenlunNativeIdentityKey = "vastora-official/pulse-agent"
const senlunServiceIdentityKey = "vastora-official/senlun"
const legacySenlunServiceIdentityKey = "vastora-official/pulse"

func senlunIdentitySource(target string) (string, string) {
	switch target {
	case senlunNativeIdentityKey:
		return legacySenlunNativeIdentityKey, "systemd"
	case senlunServiceIdentityKey:
		return legacySenlunServiceIdentityKey, "docker"
	default:
		return "", ""
	}
}

func senlunIdentityRequest(request DeploymentRequest) bool {
	source, _ := senlunIdentitySource(request.AppKey)
	return source != "" && request.ReplaceApplicationID != "" && request.SourceReceiptSHA256 != "" && request.Operation == "install"
}

// An offline collector may return after the catalog has advanced. Rename only
// with an accepted same-version recipe; upgrading the executable is a separate
// operation. Retained deployment bytes are never trusted without the immutable
// catalog digest and the original instance's current resource receipt.
func (s *Store) senlunNativeIdentityManifest(ctx context.Context, request DeploymentRequest, current catalog.AppManifest) (catalog.AppManifest, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return catalog.AppManifest{}, err
	}
	defer tx.Rollback()
	if err := authorizeOfficialManifest(ctx, tx, "stable", current, s.now().UTC()); err != nil {
		return catalog.AppManifest{}, err
	}
	if _, err := validateSenlunIdentitySource(ctx, tx, request.ReplaceApplicationID, request.SourceReceiptSHA256, current, false); err == nil {
		return current, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT d.manifest_json,d.package_revision FROM deployments d
		WHERE d.app_key=? AND d.state='succeeded' AND d.operation IN ('install','upgrade','configure')
		AND d.app_version=(SELECT old.app_version FROM deployments old WHERE old.application_id=? AND old.app_key=? AND old.state='succeeded' ORDER BY old.created_at DESC,old.rowid DESC LIMIT 1)
		ORDER BY d.package_revision DESC`, senlunNativeIdentityKey, request.ReplaceApplicationID, legacySenlunNativeIdentityKey)
	if err != nil {
		return catalog.AppManifest{}, err
	}
	var candidates []catalog.AppManifest
	for rows.Next() {
		var raw []byte
		var revision int
		if err := rows.Scan(&raw, &revision); err != nil {
			rows.Close()
			return catalog.AppManifest{}, err
		}
		var candidate catalog.AppManifest
		if json.Unmarshal(raw, &candidate) == nil && candidate.PackageRevision == revision {
			candidates = append(candidates, candidate)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return catalog.AppManifest{}, err
	}
	for _, candidate := range candidates {
		if err := authorizeSenlunNativeIdentity(ctx, tx, request.ReplaceApplicationID, request.SourceReceiptSHA256, candidate, s.now().UTC(), false); err == nil {
			return candidate, nil
		}
	}
	return catalog.AppManifest{}, errors.New("center: no verified same-version Senlun identity recipe is retained; review the original installation")
}

func authorizeSenlunNativeIdentity(ctx context.Context, tx *sql.Tx, applicationID, sourceSHA string, target catalog.AppManifest, now time.Time, allowPending bool) error {
	if "vastora-official/"+target.ID != senlunNativeIdentityKey {
		return errors.New("center: historical identity recipes are only allowed for Senlun collectors")
	}
	value, _, err := readAcceptedOfficialCatalog(ctx, tx, "stable")
	if err != nil {
		return err
	}
	available := false
	for _, current := range value.Apps {
		if current.ID != target.ID || current.Runtime == nil || current.Runtime.Kind != "systemd" {
			continue
		}
		comparison := semver.Compare(canonicalAppVersion(current.Version), canonicalAppVersion(target.Version))
		if comparison < 0 || comparison == 0 && current.PackageRevision < target.PackageRevision {
			break
		}
		if err := authorizeOfficialManifest(ctx, tx, "stable", current, now); err != nil {
			return err
		}
		available = true
		break
	}
	if !available {
		return errors.New("center: Senlun collector is no longer authorized by the current catalog")
	}
	_, _, digest, err := canonicalPackage(target)
	if err != nil {
		return err
	}
	var acceptedSHA string
	if err := tx.QueryRowContext(ctx, `SELECT manifest_sha256 FROM catalog_manifest_history WHERE source_id=? AND app_id=? AND version=? AND package_revision=?`, OfficialCatalogSourceID, target.ID, target.Version, target.PackageRevision).Scan(&acceptedSHA); err != nil || acceptedSHA != digest {
		return errors.New("center: retained Senlun identity recipe does not match accepted catalog history")
	}
	_, err = validateSenlunIdentitySource(ctx, tx, applicationID, sourceSHA, target, allowPending)
	return err
}

// Check both the signed historical deployment and the current Agent receipt.
// A changed artifact, permission or runtime is not an identity-only migration.
func validateSenlunIdentitySource(ctx context.Context, tx *sql.Tx, applicationID, expectedSHA string, target catalog.AppManifest, allowPending bool) (string, error) {
	sourceKey, runtime := senlunIdentitySource("vastora-official/" + target.ID)
	if sourceKey == "" {
		return "", errors.New("center: unsupported Senlun identity migration")
	}
	var nodeID, status, adoption, version string
	var rawReceipt, rawManifest []byte
	var revision int
	err := tx.QueryRowContext(ctx, `SELECT a.node_id,a.status,r.adoption_state,r.resources_json,d.manifest_json,d.app_version,d.package_revision
		FROM applications a JOIN application_resources r ON r.application_id=a.id
		JOIN deployments d ON d.rowid=(SELECT old.rowid FROM deployments old WHERE old.application_id=a.id AND old.app_key=a.app_key AND old.state='succeeded' ORDER BY old.created_at DESC,old.rowid DESC LIMIT 1)
		WHERE a.id=? AND a.app_key=? AND d.operation IN ('install','upgrade','configure')`, applicationID, sourceKey).Scan(&nodeID, &status, &adoption, &rawReceipt, &rawManifest, &version, &revision)
	if err != nil || (allowPending && status != "pending" && status != "deploying") || (!allowPending && (status == "pending" || status == "deploying")) || adoption != "ready" {
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
	if len(rawReceipt) > 1<<20 || hex.EncodeToString(digest[:]) != expectedSHA || json.Unmarshal(rawReceipt, &receipt) != nil || receipt.Version != 1 || receipt.ApplicationID != applicationID || receipt.AppKey != sourceKey || receipt.Runtime != runtime || receipt.State != "ready" || receipt.PackageVersion != version || receipt.PackageRevision != revision {
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

func (s *Store) prepareSenlunIdentity(ctx context.Context, tx *sql.Tx, request DeploymentRequest, target catalog.AppManifest, now time.Time) (string, error) {
	nodeID, err := validateSenlunIdentitySource(ctx, tx, request.ReplaceApplicationID, request.SourceReceiptSHA256, target, false)
	if err != nil || nodeID != request.AgentID {
		return "", errors.New("center: Senlun identity source or target node differs")
	}
	if err := requireNodeRuntime(ctx, tx, nodeID, target); err != nil {
		return "", err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM deployments WHERE application_id=? AND (state IN ('pending','running') OR reconciliation_required=1))+(SELECT COUNT(*) FROM application_commands WHERE application_id=? AND state IN ('pending','running'))+(SELECT COUNT(*) FROM applications WHERE node_id=? AND app_key=? AND id<>?)`, request.ReplaceApplicationID, request.ReplaceApplicationID, nodeID, request.AppKey, request.ReplaceApplicationID).Scan(&active); err != nil {
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

func completeSenlunIdentity(ctx context.Context, tx *sql.Tx, applicationID, expectedSHA string, targetRaw []byte, now time.Time) error {
	var target catalog.AppManifest
	if json.Unmarshal(targetRaw, &target) != nil {
		return errors.New("center: authorized Senlun target recipe is invalid")
	}
	targetKey := "vastora-official/" + target.ID
	sourceKey, _ := senlunIdentitySource(targetKey)
	if sourceKey == "" {
		return errors.New("center: authorized Senlun target recipe is invalid")
	}
	// prepare marked this same instance pending. Recheck the original receipt
	// and signed recipe in the same transaction as the identity update.
	if _, err := validateSenlunIdentitySource(ctx, tx, applicationID, expectedSHA, target, true); err != nil {
		return err
	}
	if err := retainNativeReplacementBindings(ctx, tx, applicationID, target); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE applications SET app_key=?,updated_at=? WHERE id=? AND app_key=?`, targetKey, now.Format(time.RFC3339Nano), applicationID, sourceKey)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return errors.New("center: Senlun application identity changed during migration")
	}
	return nil
}
