package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/petauron/catalog/catalog"
)

// Restore exactly the saved package identity and approvals. Recovery never
// grants capabilities merely because a newer recipe requests them.
func readReinstallPackageAuthorization(ctx context.Context, tx *sql.Tx, deploymentID string, task *AgentTask) error {
	var grants []byte
	if err := tx.QueryRowContext(ctx, `SELECT package_revision,manifest_sha256,authorized_capabilities_json FROM deployments WHERE id=?`, deploymentID).Scan(&task.PackageRevision, &task.ManifestSHA256, &grants); err != nil {
		return err
	}
	if json.Unmarshal(grants, &task.AuthorizedCapabilities) != nil {
		return errors.New("center: saved package permissions are invalid")
	}
	_, _, digest, err := canonicalPackage(task.Manifest)
	if err != nil || digest != task.ManifestSHA256 || task.PackageRevision != task.Manifest.PackageRevision {
		return errors.New("center: saved package identity requires review")
	}
	return catalog.ValidateAuthorizedCapabilities(task.Manifest, task.AuthorizedCapabilities)
}

func copyReinstallPackageAuthorization(ctx context.Context, tx *sql.Tx, sourceID, targetID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE deployments SET (package_revision,manifest_sha256,authorized_capabilities_json)=(SELECT package_revision,manifest_sha256,authorized_capabilities_json FROM deployments WHERE id=?) WHERE id=?`, sourceID, targetID)
	return err
}
