package center

import (
	"context"
	"errors"

	"github.com/petauron/catalog/catalog"
)

// A failed website can reapply its exact installed, verified recipe through the
// normal upgrade transaction. Healthy installs and other products remain gated;
// Agent ownership checks, backup, handover and gateway application still run.
func (s *Store) failedDaylightReapplyAllowed(ctx context.Context, request DeploymentRequest, manifest catalog.AppManifest, active activeDeploymentState) (bool, error) {
	if request.AppKey != "vastora-official/daylight" && request.AppKey != "vastora-official/daylight-https" {
		return false, nil
	}
	if manifest.Runtime == nil || manifest.Runtime.Kind != "systemd" || manifest.Runtime.Version != 2 {
		return false, nil
	}
	changes, err := decodeJSONObject(request.Config, "center: website reapply requires an empty configuration")
	if err != nil || len(changes) != 0 {
		return false, errors.New("center: website reapply cannot modify installed settings")
	}
	_, _, digest, err := canonicalPackage(manifest)
	if err != nil {
		return false, err
	}
	if digest != rawManifestDigest(active.Manifest) {
		return false, errors.New("center: website reapply requires the exact installed recipe")
	}
	var allowed bool
	err = s.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM deployments installed
		JOIN applications a ON a.id=installed.application_id
		JOIN application_resources r ON r.application_id=a.id
		WHERE installed.id=? AND a.node_id=? AND a.app_key=? AND a.status='failed'
		AND r.adoption_state='ready' AND r.manifest_sha256=?
		AND json_extract(r.resources_json,'$.state')='ready'
		AND json_extract(r.resources_json,'$.applicationId')=a.id
		AND json_extract(r.resources_json,'$.appKey')=a.app_key
		AND json_extract(r.resources_json,'$.manifestSha256')=r.manifest_sha256
	)`, active.ID, request.AgentID, request.AppKey, digest).Scan(&allowed)
	return allowed, err
}
