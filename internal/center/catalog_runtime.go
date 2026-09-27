package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/petauron/catalog/catalog"
)

// Runtime support is independent of application identity. Future, correctly
// signed packages remain visible even if this Center cannot execute them.
func requireCatalogRuntime(manifest catalog.AppManifest) error {
	if err := catalog.ValidateApp(manifest); err != nil {
		return err
	}
	result := catalog.CheckRuntimeSupport(manifest, catalog.ExecutorSupport{
		Versions:     map[string]int{"docker": 1, "systemd": 1},
		Capabilities: []string{"root", "host-network", "host-path", "devices", "meridian-runtime"},
	})
	if !result.Supported {
		return fmt.Errorf("center: unsupported package: %s", strings.Join(result.Reasons, "; "))
	}
	return nil
}

func requireNodeRuntime(ctx context.Context, db registryCredentialQuerier, agentID string, manifest catalog.AppManifest) error {
	if err := requireCatalogRuntime(manifest); err != nil {
		return err
	}
	var raw []byte
	if err := db.QueryRowContext(ctx, `SELECT capabilities_json FROM agents WHERE id=?`, agentID).Scan(&raw); err != nil {
		return fmt.Errorf("center: read node runtime capabilities: %w", err)
	}
	var capabilities NodeCapabilities
	if json.Unmarshal(raw, &capabilities) != nil {
		return errors.New("center: node runtime capabilities are invalid")
	}
	result := catalog.CheckRuntimeSupport(manifest, catalog.ExecutorSupport{Versions: capabilities.ExecutorVersions, Capabilities: capabilities.RuntimeCapabilities})
	if !result.Supported {
		return fmt.Errorf("center: target node cannot run this package: %s", strings.Join(result.Reasons, "; "))
	}
	return nil
}

func canonicalPackage(manifest catalog.AppManifest) (catalog.AppManifest, []byte, string, error) {
	canonical, err := catalog.CanonicalAppManifest(manifest)
	if err != nil {
		return catalog.AppManifest{}, nil, "", err
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return catalog.AppManifest{}, nil, "", err
	}
	digest := sha256.Sum256(raw)
	return canonical, raw, hex.EncodeToString(digest[:]), nil
}

func rawManifestDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// Historical packages are audit evidence only. Removal uses the adopted
// instance identity and its executor, never the old application installer.
func legacyRemovalAuthorization(ctx context.Context, db registryCredentialQuerier, nodeID, appKey string, raw []byte) ([]string, error) {
	var manifest catalog.AppManifest
	if json.Unmarshal(raw, &manifest) != nil || manifest.Runtime != nil || manifest.PackageRevision != 0 {
		return nil, errors.New("center: invalid historical removal manifest")
	}
	var applicationID, digest, state string
	var revision int
	var receiptRaw, grantsRaw, capabilitiesRaw []byte
	if err := db.QueryRowContext(ctx, `SELECT a.id,r.manifest_sha256,r.package_revision,r.adoption_state,r.resources_json,r.authorized_capabilities_json,n.capabilities_json
		FROM applications a JOIN application_resources r ON r.application_id=a.id JOIN agents n ON n.id=a.node_id WHERE a.node_id=? AND a.app_key=?`, nodeID, appKey).Scan(&applicationID, &digest, &revision, &state, &receiptRaw, &grantsRaw, &capabilitiesRaw); err != nil {
		return nil, errors.New("center: adopt historical resources before uninstalling this application")
	}
	var receipt struct {
		Version                int               `json:"version"`
		ApplicationID          string            `json:"applicationId"`
		AppKey                 string            `json:"appKey"`
		Runtime                string            `json:"runtime"`
		PackageVersion         string            `json:"packageVersion"`
		PackageRevision        int               `json:"packageRevision"`
		ManifestSHA256         string            `json:"manifestSha256"`
		State                  string            `json:"state"`
		AuthorizedCapabilities []string          `json:"authorizedCapabilities"`
		Resources              []json.RawMessage `json:"resources"`
	}
	var grants []string
	if state != "ready" || revision != 0 || digest != rawManifestDigest(raw) || json.Unmarshal(receiptRaw, &receipt) != nil || json.Unmarshal(grantsRaw, &grants) != nil || receipt.Version != 1 || receipt.ApplicationID != applicationID || receipt.AppKey != appKey || receipt.PackageVersion != manifest.Version || receipt.PackageRevision != 0 || receipt.ManifestSHA256 != digest || receipt.State != "ready" || len(receipt.Resources) == 0 {
		return nil, errors.New("center: historical resource ownership is not confirmed")
	}
	slices.Sort(grants)
	slices.Sort(receipt.AuthorizedCapabilities)
	if !slices.Equal(grants, receipt.AuthorizedCapabilities) {
		return nil, errors.New("center: historical permission evidence mismatch")
	}
	var capabilities NodeCapabilities
	if json.Unmarshal(capabilitiesRaw, &capabilities) != nil || receipt.Runtime != "docker" && receipt.Runtime != "systemd" || capabilities.ExecutorVersions[receipt.Runtime] != 1 {
		return nil, errors.New("center: target node cannot remove the adopted runtime")
	}
	return grants, nil
}

func packageRuntimeName(manifest catalog.AppManifest) string {
	if manifest.Runtime != nil && manifest.Runtime.Kind == "systemd" {
		return "host"
	}
	return "docker"
}

func deploymentPermissions(ctx context.Context, db registryCredentialQuerier, request DeploymentRequest, manifest catalog.AppManifest) ([]string, error) {
	grants := []string{}
	var state string
	var previous []byte
	err := db.QueryRowContext(ctx, `SELECT r.adoption_state, r.authorized_capabilities_json
		FROM application_resources r JOIN applications a ON a.id=r.application_id
		WHERE a.node_id=? AND a.app_key=?`, request.AgentID, request.AppKey).Scan(&state, &previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		if state != "ready" {
			return nil, errors.New("center: verify and adopt existing resources in the maintenance window before changing this application")
		}
		if json.Unmarshal(previous, &grants) != nil {
			return nil, errors.New("center: stored package permissions are invalid")
		}
	}
	if request.AuthorizedCapabilities != nil {
		grants = slices.Clone(*request.AuthorizedCapabilities)
	} else if manifest.Runtime != nil {
		// Capability names alone cannot describe scope: an existing host-path
		// grant must not silently cover a new mount/device/root process in a new
		// recipe. Privileged upgrades always require reviewing the pinned recipe.
		if request.Operation == "upgrade" && len(manifest.Runtime.RequiredCapabilities) != 0 {
			return nil, errors.New("center: administrator permission approval required: review the new recipe's privilege scope before upgrading")
		}
		// Removal of privileges is safe without new approval; additions are not.
		grants = slices.DeleteFunc(grants, func(value string) bool { return !slices.Contains(manifest.Runtime.RequiredCapabilities, value) })
	}
	if err := catalog.ValidateAuthorizedCapabilities(manifest, grants); err != nil {
		return nil, fmt.Errorf("center: administrator permission approval required: %w", err)
	}
	slices.Sort(grants)
	return grants, nil
}

func packagePermissionDetails(manifest catalog.AppManifest) []string {
	var details []string
	if manifest.Runtime == nil {
		return details
	}
	if docker := manifest.Runtime.Docker; docker != nil {
		for _, c := range docker.Containers {
			if c.User == "root" || c.User == "0" || strings.HasPrefix(c.User, "root:") || strings.HasPrefix(c.User, "0:") {
				details = append(details, c.Name+": user "+c.User)
			}
			if c.HostNetwork {
				details = append(details, c.Name+": host network")
			}
			for _, m := range c.Mounts {
				if m.HostPath != "" {
					access := "read/write"
					if m.ReadOnly {
						access = "read-only"
					}
					details = append(details, c.Name+": "+m.HostPath+" -> "+m.Target+" ("+access+")")
				}
			}
			for _, d := range c.Devices {
				details = append(details, c.Name+": device "+d)
			}
		}
	}
	if native := manifest.Runtime.Systemd; native != nil && (native.User == "0" || native.User == "root") {
		details = append(details, "systemd: user "+native.User)
	}
	return details
}

// Resource receipts contain only ownership and digest metadata. Secret values
// belong in the existing encrypted delivery channel, never in this record.
func storePackageReceipt(ctx context.Context, tx *sql.Tx, taskID, applicationID, appKey, operation string, raw json.RawMessage, now time.Time) error {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return errors.New("center: missing or oversized package resource receipt")
	}
	var receipt struct {
		Version                int               `json:"version"`
		ApplicationID          string            `json:"applicationId"`
		AppKey                 string            `json:"appKey"`
		TaskID                 string            `json:"taskId"`
		PackageVersion         string            `json:"packageVersion"`
		PackageRevision        int               `json:"packageRevision"`
		ManifestSHA256         string            `json:"manifestSha256"`
		AuthorizedCapabilities []string          `json:"authorizedCapabilities"`
		State                  string            `json:"state"`
		Runtime                string            `json:"runtime"`
		Resources              []json.RawMessage `json:"resources"`
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.Version != 1 || receipt.ApplicationID != applicationID || receipt.AppKey != appKey || receipt.TaskID != taskID {
		return errors.New("center: package resource receipt identity mismatch")
	}
	if receipt.Runtime != "docker" && receipt.Runtime != "systemd" || receipt.State == "ready" && len(receipt.Resources) == 0 {
		return errors.New("center: package resource receipt lacks runtime ownership evidence")
	}
	if operation == "uninstall" {
		if receipt.State != "retained" && receipt.State != "removed" {
			return errors.New("center: package resource receipt does not confirm removal")
		}
	} else if receipt.State != "ready" {
		return errors.New("center: package resource receipt does not confirm health")
	}
	var version, digest string
	var revision int
	var grantsRaw, manifestRaw []byte
	if err := tx.QueryRowContext(ctx, `SELECT app_version,package_revision,manifest_sha256,authorized_capabilities_json,manifest_json FROM deployments WHERE id=? AND application_id=?`, taskID, applicationID).Scan(&version, &revision, &digest, &grantsRaw, &manifestRaw); err != nil {
		return err
	}
	var manifest catalog.AppManifest
	if json.Unmarshal(manifestRaw, &manifest) != nil || revision > 0 && (manifest.Runtime == nil || manifest.Runtime.Kind != receipt.Runtime) {
		return errors.New("center: package resource receipt runtime differs from the authorized recipe")
	}
	var grants []string
	if json.Unmarshal(grantsRaw, &grants) != nil {
		return errors.New("center: invalid deployment permission record")
	}
	slices.Sort(grants)
	slices.Sort(receipt.AuthorizedCapabilities)
	if version != receipt.PackageVersion || revision != receipt.PackageRevision || digest == "" || digest != receipt.ManifestSHA256 || !slices.Equal(grants, receipt.AuthorizedCapabilities) {
		return errors.New("center: package resource receipt does not match the authorized deployment")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO application_resources(application_id,manifest_sha256,package_revision,authorized_capabilities_json,resources_json,adoption_state,updated_at)
		VALUES(?,?,?,?,?,'ready',?) ON CONFLICT(application_id) DO UPDATE SET manifest_sha256=excluded.manifest_sha256,package_revision=excluded.package_revision,
		authorized_capabilities_json=excluded.authorized_capabilities_json,resources_json=excluded.resources_json,adoption_state='ready',last_error='',updated_at=excluded.updated_at`, applicationID, digest, revision, grantsRaw, raw, now.Format(time.RFC3339Nano))
	return err
}

// Product commands can change concrete runtime resources, but cannot change
// the installed package identity or expand its administrator-approved grants.
func storeIntegratedPackageReceipt(ctx context.Context, tx *sql.Tx, taskID, applicationID, appKey string, raw json.RawMessage, now time.Time) error {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return errors.New("center: missing or oversized integrated resource receipt")
	}
	type identity struct {
		Version                int               `json:"version"`
		ApplicationID          string            `json:"applicationId"`
		AppKey                 string            `json:"appKey"`
		TaskID                 string            `json:"taskId"`
		PackageVersion         string            `json:"packageVersion"`
		PackageRevision        int               `json:"packageRevision"`
		ManifestSHA256         string            `json:"manifestSha256"`
		Runtime                string            `json:"runtime"`
		State                  string            `json:"state"`
		AuthorizedCapabilities []string          `json:"authorizedCapabilities"`
		Resources              []json.RawMessage `json:"resources"`
	}
	var next, previous identity
	var previousRaw, grantsRaw []byte
	var digest, adoption string
	var revision int
	if err := tx.QueryRowContext(ctx, `SELECT manifest_sha256,package_revision,authorized_capabilities_json,resources_json,adoption_state FROM application_resources WHERE application_id=?`, applicationID).Scan(&digest, &revision, &grantsRaw, &previousRaw, &adoption); err != nil {
		return err
	}
	if adoption != "ready" || json.Unmarshal(previousRaw, &previous) != nil || json.Unmarshal(raw, &next) != nil || next.Version != 1 || next.ApplicationID != applicationID || next.AppKey != appKey || next.TaskID != taskID || next.State != "ready" || len(next.Resources) == 0 || next.PackageVersion != previous.PackageVersion || next.PackageRevision != revision || next.ManifestSHA256 != digest || next.Runtime != previous.Runtime {
		return errors.New("center: product command changed the installed package identity or returned unverified resources")
	}
	var grants []string
	if json.Unmarshal(grantsRaw, &grants) != nil {
		return errors.New("center: invalid installed permission record")
	}
	slices.Sort(grants)
	slices.Sort(next.AuthorizedCapabilities)
	if !slices.Equal(grants, next.AuthorizedCapabilities) {
		return errors.New("center: product command cannot expand package permissions")
	}
	_, err := tx.ExecContext(ctx, `UPDATE application_resources SET resources_json=?,updated_at=? WHERE application_id=?`, raw, now.UTC().Format(time.RFC3339Nano), applicationID)
	return err
}
