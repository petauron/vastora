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
	"strconv"
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
		Versions:     map[string]int{"docker": 1, "systemd": 2, "native": 2},
		Capabilities: []string{"root", "host-network", "host-path", "devices", "meridian-runtime", "native-services", "shared-caddy", "host-tcp-budget", "cpa-tunnel-identity"},
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
	if json.Unmarshal(capabilitiesRaw, &capabilities) != nil {
		return nil, errors.New("center: target node cannot remove the adopted runtime")
	}
	// The systemd v2 executor still removes adopted v1 single-service receipts.
	// Reject unknown versions instead of assuming future compatibility.
	version := capabilities.ExecutorVersions[receipt.Runtime]
	supported := receipt.Runtime == "docker" && version == 1 ||
		receipt.Runtime == "systemd" && (version == 1 || version == 2)
	if !supported {
		return nil, errors.New("center: target node cannot remove the adopted runtime")
	}
	return grants, nil
}

func packageRuntimeName(manifest catalog.AppManifest) string {
	if manifest.Runtime != nil && (manifest.Runtime.Kind == "systemd" || manifest.Runtime.Kind == "native") {
		return "host"
	}
	return "docker"
}

// Built-in platform capabilities describe runtime/platform integration gates
// verified between Center and Agent rather than privileged host escalations
// that require manual administrator checkboxes.
func isBuiltInPlatformCapability(appKey, capability string) bool {
	if capability == "native-services" {
		return true
	}
	if !strings.HasPrefix(appKey, OfficialCatalogSourceID+"/") {
		return false
	}
	switch capability {
	case "shared-caddy", "cpa-tunnel-identity", "host-tcp-budget", "meridian-runtime":
		return true
	default:
		return false
	}
}

func approvalRequiredCapabilities(appKey string, manifest catalog.AppManifest) []string {
	if manifest.Runtime == nil || len(manifest.Runtime.RequiredCapabilities) == 0 {
		return nil
	}
	var required []string
	for _, capability := range manifest.Runtime.RequiredCapabilities {
		if !isBuiltInPlatformCapability(appKey, capability) {
			required = append(required, capability)
		}
	}
	return required
}

func isZeroID(part string) bool {
	if part == "root" {
		return true
	}
	numeric, err := strconv.ParseUint(part, 10, 32)
	return err == nil && numeric == 0
}

func rootProcessPrivileges(user string) (uidRoot, gidRoot bool) {
	if user == "" {
		return false, false
	}
	parts := strings.Split(user, ":")
	if len(parts) == 1 {
		root := isZeroID(parts[0])
		return root, root
	}
	return isZeroID(parts[0]), isZeroID(parts[1])
}

func isRootProcessUser(user string) bool {
	uidRoot, gidRoot := rootProcessPrivileges(user)
	return uidRoot || gidRoot
}

func appendRootScopes(scopes map[string][]string, prefix, user string) {
	uidRoot, gidRoot := rootProcessPrivileges(user)
	if uidRoot {
		scopes["root"] = append(scopes["root"], prefix+":uid0")
	}
	if gidRoot {
		scopes["root"] = append(scopes["root"], prefix+":gid0")
	}
}

func packageCapabilityScopes(manifest catalog.AppManifest) map[string][]string {
	scopes := map[string][]string{}
	if manifest.Runtime == nil {
		return scopes
	}
	if docker := manifest.Runtime.Docker; docker != nil {
		for _, c := range docker.Containers {
			appendRootScopes(scopes, "docker:"+c.Name, c.User)
			if c.HostNetwork {
				scopes["host-network"] = append(scopes["host-network"], "docker:"+c.Name+":host-network")
			}
			for _, m := range c.Mounts {
				if m.HostPath != "" {
					access := "rw"
					if m.ReadOnly {
						access = "ro"
					}
					scopes["host-path"] = append(scopes["host-path"], "docker:"+c.Name+":"+m.HostPath+"->"+m.Target+":"+access)
				}
			}
			for _, d := range c.Devices {
				scopes["devices"] = append(scopes["devices"], "docker:"+c.Name+":device:"+d)
			}
		}
	}
	if native := manifest.Runtime.Systemd; native != nil {
		appendRootScopes(scopes, "systemd", native.User)
	}
	if bundle := manifest.Runtime.NativeServices; bundle != nil {
		for _, service := range bundle.Services {
			if service.ManagedUser == "" {
				appendRootScopes(scopes, "native:"+service.Name, service.Process.User)
			}
		}
	}
	return scopes
}

func capabilityScopeCovered(capability string, grantedScopes, targetScopes []string) bool {
	for _, target := range targetScopes {
		if slices.Contains(grantedScopes, target) {
			continue
		}
		if capability == "host-path" && strings.HasSuffix(target, ":ro") &&
			slices.Contains(grantedScopes, strings.TrimSuffix(target, ":ro")+":rw") {
			continue
		}
		return false
	}
	return true
}

func capabilityAlreadyApprovedForUpgrade(capability string, installed *catalog.AppManifest, existingGrants []string, target catalog.AppManifest) bool {
	if installed == nil || installed.Runtime == nil || !slices.Contains(existingGrants, capability) {
		return false
	}
	switch capability {
	case "root", "host-network", "host-path", "devices":
	default:
		// Unknown or unmodeled capabilities fail closed and require explicit approval.
		return false
	}
	installedScopes := packageCapabilityScopes(*installed)[capability]
	targetScopes := packageCapabilityScopes(target)[capability]
	return capabilityScopeCovered(capability, installedScopes, targetScopes)
}

func upgradeApprovalCapabilities(appKey string, installed *catalog.AppManifest, existingGrants []string, target catalog.AppManifest) []string {
	var pending []string
	for _, capability := range approvalRequiredCapabilities(appKey, target) {
		if !capabilityAlreadyApprovedForUpgrade(capability, installed, existingGrants, target) {
			pending = append(pending, capability)
		}
	}
	return pending
}

func deploymentPermissions(ctx context.Context, db registryCredentialQuerier, request DeploymentRequest, manifest catalog.AppManifest) ([]string, error) {
	existingGrants := []string{}
	var state string
	var previous, installedRaw []byte
	err := db.QueryRowContext(ctx, `SELECT r.adoption_state, r.authorized_capabilities_json, COALESCE(latest.manifest_json, '')
		FROM applications a
		JOIN application_resources r ON r.application_id=a.id
		LEFT JOIN deployments latest ON latest.rowid = (
			SELECT candidate.rowid FROM deployments candidate
			WHERE candidate.agent_id = a.node_id AND candidate.application_id = a.id AND candidate.state = 'succeeded'
			ORDER BY candidate.created_at DESC, candidate.rowid DESC LIMIT 1
		)
		WHERE a.node_id=? AND a.app_key=?`, request.AgentID, request.AppKey).Scan(&state, &previous, &installedRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var installed *catalog.AppManifest
	if err == nil {
		if state != "ready" {
			return nil, errors.New("center: verify and adopt existing resources in the maintenance window before changing this application")
		}
		if json.Unmarshal(previous, &existingGrants) != nil {
			return nil, errors.New("center: stored package permissions are invalid")
		}
		if len(installedRaw) > 0 {
			var parsed catalog.AppManifest
			if json.Unmarshal(installedRaw, &parsed) == nil && parsed.Runtime != nil {
				installed = &parsed
			}
		}
	}
	required := []string{}
	if manifest.Runtime != nil && len(manifest.Runtime.RequiredCapabilities) > 0 {
		required = slices.Clone(manifest.Runtime.RequiredCapabilities)
	}
	explicit := map[string]bool{}
	if request.AuthorizedCapabilities != nil {
		for _, capability := range *request.AuthorizedCapabilities {
			if capability == "" || explicit[capability] {
				return nil, errors.New("center: administrator permission approval required: catalog: invalid or duplicate capability authorization")
			}
			if !slices.Contains(required, capability) {
				return nil, errors.New("center: administrator permission approval required: catalog: extra capability authorizations are forbidden")
			}
			explicit[capability] = true
		}
	}
	var pending []string
	switch request.Operation {
	case "install":
		pending = approvalRequiredCapabilities(request.AppKey, manifest)
	case "upgrade":
		pending = upgradeApprovalCapabilities(request.AppKey, installed, existingGrants, manifest)
	case "configure", "uninstall":
		for _, capability := range approvalRequiredCapabilities(request.AppKey, manifest) {
			if !slices.Contains(existingGrants, capability) {
				pending = append(pending, capability)
			}
		}
	default:
		pending = approvalRequiredCapabilities(request.AppKey, manifest)
	}
	for _, capability := range pending {
		if !explicit[capability] {
			return nil, fmt.Errorf("center: administrator permission approval required: capability %s requires explicit authorization", capability)
		}
	}
	if err := catalog.ValidateAuthorizedCapabilities(manifest, required); err != nil {
		return nil, fmt.Errorf("center: administrator permission approval required: %w", err)
	}
	slices.Sort(required)
	return required, nil
}

func packagePermissionDetailsByCapability(manifest catalog.AppManifest) map[string][]string {
	details := map[string][]string{}
	if manifest.Runtime == nil {
		return details
	}
	if docker := manifest.Runtime.Docker; docker != nil {
		for _, c := range docker.Containers {
			if isRootProcessUser(c.User) {
				details["root"] = append(details["root"], c.Name+": user "+c.User)
			}
			if c.HostNetwork {
				details["host-network"] = append(details["host-network"], c.Name+": host network")
			}
			for _, m := range c.Mounts {
				if m.HostPath != "" {
					access := "read/write"
					if m.ReadOnly {
						access = "read-only"
					}
					details["host-path"] = append(details["host-path"], c.Name+": "+m.HostPath+" -> "+m.Target+" ("+access+")")
				}
			}
			for _, d := range c.Devices {
				details["devices"] = append(details["devices"], c.Name+": device "+d)
			}
		}
	}
	if native := manifest.Runtime.Systemd; native != nil && isRootProcessUser(native.User) {
		details["root"] = append(details["root"], "systemd: user "+native.User)
	}
	if bundle := manifest.Runtime.NativeServices; bundle != nil {
		for _, service := range bundle.Services {
			if service.ManagedUser == "" && isRootProcessUser(service.Process.User) {
				details["root"] = append(details["root"], service.Name+": user "+service.Process.User)
			}
		}
	}
	return details
}

func packagePermissionDetails(manifest catalog.AppManifest) []string {
	byCapability := packagePermissionDetailsByCapability(manifest)
	var details []string
	for _, capability := range []string{"root", "host-network", "host-path", "devices"} {
		details = append(details, byCapability[capability]...)
	}
	return details
}

func upgradePermissionDetails(installed *catalog.AppManifest, target catalog.AppManifest, pending []string) []string {
	if len(pending) == 0 {
		return nil
	}
	targetByCap := packagePermissionDetailsByCapability(target)
	var installedByCap map[string][]string
	if installed != nil {
		installedByCap = packagePermissionDetailsByCapability(*installed)
	}
	var details []string
	for _, capability := range pending {
		targetLines := targetByCap[capability]
		if len(targetLines) == 0 {
			continue
		}
		if installedByCap == nil {
			details = append(details, targetLines...)
			continue
		}
		priorLines := installedByCap[capability]
		var added []string
		for _, line := range targetLines {
			if !slices.Contains(priorLines, line) {
				added = append(added, line)
			}
		}
		if len(added) > 0 {
			details = append(details, added...)
		} else {
			details = append(details, targetLines...)
		}
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
		IntegrationState       string            `json:"integrationState"`
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
		RuntimeVersion         int               `json:"runtimeVersion"`
		Resources              []json.RawMessage `json:"resources"`
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.Version != 1 || receipt.ApplicationID != applicationID || receipt.AppKey != appKey || receipt.TaskID != taskID {
		return errors.New("center: package resource receipt identity mismatch")
	}
	if receipt.Runtime != "docker" && receipt.Runtime != "systemd" && receipt.Runtime != "native" || receipt.State == "ready" && len(receipt.Resources) == 0 {
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
	installedRuntimeVersion := receipt.RuntimeVersion
	if installedRuntimeVersion == 0 {
		installedRuntimeVersion = 1
	}
	if revision > 0 && manifest.Runtime.Version != installedRuntimeVersion {
		return errors.New("center: package receipt runtime contract differs from the authorized recipe")
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
	if appKey == glivelaAppKey && operation == "upgrade" {
		dormant, err := dormantGlivelaUpgrade(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if dormant {
			var image struct {
				Kind        string `json:"kind"`
				LogicalName string `json:"logicalName"`
				Name        string `json:"name"`
				SHA256      string `json:"sha256"`
			}
			if receipt.IntegrationState != "awaiting-configuration" || len(receipt.Resources) != 1 || json.Unmarshal(receipt.Resources[0], &image) != nil || image.Kind != "image" || image.LogicalName != "xray-core" || len(manifest.Images) != 1 || manifest.Images[0].Name != image.LogicalName || manifest.Images[0].Reference != image.Name || !strings.HasSuffix(image.Name, "@sha256:"+image.SHA256) || len(image.SHA256) != 64 {
				return errors.New("center: dormant Glivela upgrade must stage only its authorized image")
			}
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO application_resources(application_id,manifest_sha256,package_revision,authorized_capabilities_json,resources_json,adoption_state,updated_at)
		VALUES(?,?,?,?,?,'ready',?) ON CONFLICT(application_id) DO UPDATE SET manifest_sha256=excluded.manifest_sha256,package_revision=excluded.package_revision,
		authorized_capabilities_json=excluded.authorized_capabilities_json,resources_json=excluded.resources_json,adoption_state='ready',last_error='',updated_at=excluded.updated_at`, applicationID, digest, revision, grantsRaw, raw, now.Format(time.RFC3339Nano))
	return err
}

// Product commands can change concrete runtime resources, but cannot change
// the installed package identity or expand its administrator-approved grants.
