package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/senulun"
)

// Senlun owns enrollment; the generic native executor owns every installed
// account, file and unit. Importing an existing identity never calls enrollment.
type senlunNativePackageBackend struct {
	*NativeServicesPackageBackend
	source      *SystemdPackageBackend
	sourceTask  DeploymentTask
	sourceState string
	identity    []byte
	nodeID      string
	serviceURL  string
}

func (e ApplicationExecutor) senlunNativeBackend(ctx context.Context, task DeploymentTask, manager SystemdHostApplicationManager) (*senlunNativePackageBackend, error) {
	b := &senlunNativePackageBackend{NativeServicesPackageBackend: &NativeServicesPackageBackend{Manager: manager, StateDirectory: e.packageDirectory(), Store: e.Store}}
	if task.Operation != "upgrade" || e.Store == nil {
		return b, nil
	}
	receipt, err := e.ApplicationResources(task.ApplicationID)
	if err != nil {
		return nil, err
	}
	version := receipt.RuntimeVersion
	if version == 0 {
		version = 1
	}
	if version != 1 {
		return b, nil
	}
	history, err := e.Store.AppliedInstallation(ctx, task.AppKey)
	if err != nil || history.ApplicationID != task.ApplicationID || history.Version != receipt.PackageVersion || history.Manifest.Runtime == nil || history.Manifest.Runtime.Kind != "systemd" || history.Manifest.Runtime.Version != 1 {
		return nil, errors.New("agent: original Senlun installation evidence is unavailable")
	}
	manifest, err := catalog.CanonicalAppManifest(history.Manifest)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	if manifest.PackageRevision != receipt.PackageRevision || hex.EncodeToString(digest[:]) != receipt.ManifestSHA256 {
		return nil, errors.New("agent: original Senlun recipe does not match its receipt")
	}
	b.source = &SystemdPackageBackend{Manager: manager, StateDirectory: e.packageDirectory()}
	b.sourceTask = task
	b.sourceTask.Manifest, b.sourceTask.PackageRevision, b.sourceTask.ManifestSHA256 = manifest, receipt.PackageRevision, receipt.ManifestSHA256
	return b, nil
}

func (b *senlunNativePackageBackend) permitsMigration(task DeploymentTask, receipt *InstanceResources) bool {
	return b.source != nil && task.Operation == "upgrade" && task.AppKey == senulun.AgentKey && receipt.AppKey == task.AppKey && receipt.ApplicationID == task.ApplicationID && receipt.Runtime == "systemd" && (receipt.RuntimeVersion == 0 || receipt.RuntimeVersion == 1) && task.Manifest.Runtime.Version == 2 && receipt.ManifestSHA256 == b.sourceTask.ManifestSHA256
}

func (b *senlunNativePackageBackend) Inspect(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if b.permitsMigration(task, receipt) {
		return b.source.Inspect(ctx, b.sourceTask, receipt)
	}
	return b.NativeServicesPackageBackend.Inspect(ctx, task, receipt)
}

func parseSenlunNativeIdentity(raw []byte, serviceURL string) (string, error) {
	var identity struct {
		ProtocolVersion int    `json:"protocol_version"`
		ServiceURL      string `json:"service_url"`
		NodeID          string `json:"node_id"`
		AgentToken      string `json:"agent_token"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var extra any
	if len(raw) == 0 || len(raw) > 8192 || decoder.Decode(&identity) != nil || decoder.Decode(&extra) != io.EOF || identity.ProtocolVersion != 2 || identity.ServiceURL != serviceURL || identity.NodeID == "" || len(identity.NodeID) > 128 || strings.ContainsAny(identity.NodeID, "\x00\r\n") || len(identity.AgentToken) < 16 || len(identity.AgentToken) > 512 || strings.ContainsAny(identity.AgentToken, " \t\r\n") {
		return "", errors.New("agent: existing Senlun identity is invalid or belongs to another service")
	}
	return identity.NodeID, nil
}

func (b *senlunNativePackageBackend) Prepare(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if task.Operation == "uninstall" {
		return b.Inspect(ctx, task, receipt)
	}
	var settings senulun.AgentConfig
	if json.Unmarshal(task.Config, &settings) != nil || settings.Validate() != nil {
		return errors.New("agent: invalid Senlun configuration")
	}
	b.serviceURL = settings.ServiceURL
	var secrets map[string]string
	if json.Unmarshal(task.Secrets, &secrets) != nil {
		return errors.New("agent: invalid Senlun inputs")
	}
	if b.permitsMigration(task, receipt) {
		if secrets["existing_identity"] != "" {
			return errors.New("agent: an upgrade cannot replace the installed Senlun identity")
		}
		retained, err := b.source.prepareSenulunInputs(task, receipt)
		if err != nil {
			return err
		}
		task = retained
		b.sourceState = filepath.Dir(senulunPackageIdentityPath(task, receipt))
		raw, err := readSenulunPackageCredentials(b.Manager.path(senulunPackageIdentityPath(task, receipt)), b.serviceURL)
		if err != nil || len(raw) == 0 {
			return errors.New("agent: retained Senlun identity is unavailable")
		}
		b.identity = raw
		receipt = nil // New resources have their own names and ownership checks.
	} else if supplied := secrets["existing_identity"]; supplied != "" {
		if task.Operation != "install" || receipt != nil && len(receipt.Resources) != 0 {
			return errors.New("agent: identity import is only allowed for a new Senlun installation")
		}
		b.identity = []byte(supplied)
	}
	if len(b.identity) != 0 {
		var err error
		b.nodeID, err = parseSenlunNativeIdentity(b.identity, b.serviceURL)
		if err != nil {
			return err
		}
	}
	var nativeSecrets map[string]string
	if json.Unmarshal(task.Secrets, &nativeSecrets) != nil || nativeSecrets == nil {
		return errors.New("agent: invalid Senlun credentials")
	}
	if _, present := nativeSecrets["enrollment_token"]; !present {
		nativeSecrets["enrollment_token"] = ""
	}
	var encodeErr error
	task.Secrets, encodeErr = json.Marshal(nativeSecrets)
	if encodeErr != nil {
		return encodeErr
	}
	if err := b.NativeServicesPackageBackend.Prepare(ctx, task, receipt); err != nil {
		return err
	}
	plan := b.prepared.Plan
	if len(plan.Units) != 1 || len(plan.State) != 1 {
		return errors.New("agent: expected one Senlun collector and identity directory")
	}
	for _, state := range plan.State {
		if b.source != nil && state.Path == b.sourceState {
			return errors.New("agent: Senlun migration must retain its original state separately")
		}
		if task.Operation != "install" && b.source == nil {
			raw, err := readSenulunPackageCredentials(b.Manager.path(filepath.Join(state.Path, "agent-credentials.json")), b.serviceURL)
			if err != nil || len(raw) == 0 {
				return errors.New("agent: retained Senlun identity is unavailable")
			}
			var parseErr error
			b.nodeID, parseErr = parseSenlunNativeIdentity(raw, b.serviceURL)
			if parseErr != nil {
				return parseErr
			}
		}
	}
	if task.Operation == "install" && len(b.identity) == 0 && (len(secrets["enrollment_token"]) < 16 || len(secrets["enrollment_token"]) > 512 || strings.ContainsAny(secrets["enrollment_token"], " \t\r\n")) {
		return errors.New("agent: supply a Senlun enrollment token or an explicitly imported existing identity")
	}
	return nil
}

func (b *senlunNativePackageBackend) retainSource(ctx context.Context, task DeploymentTask, receipt *InstanceResources, persist func() error) error {
	if err := b.source.Inspect(ctx, b.sourceTask, receipt); err != nil {
		return err
	}
	unit := resourceNamed(receipt, "unit", "service")
	if unit == nil {
		return errors.New("agent: original Senlun service receipt is missing")
	}
	if err := b.Manager.run(ctx, "systemctl", "stop", unit.Name); err != nil {
		return err
	}
	observed, err := b.Manager.readPackageUnit(ctx, unit.Name)
	if err != nil || observed.MainPID != 0 {
		return errors.New("agent: original Senlun writer is still active")
	}
	raw, err := readSenulunPackageCredentials(b.Manager.path(senulunPackageIdentityPath(b.sourceTask, receipt)), b.serviceURL)
	if err != nil || !bytes.Equal(raw, b.identity) {
		return errors.New("agent: original Senlun identity changed before handover")
	}
	// One bounded recovery record: owned configuration, unit and identity only.
	// The binary remains reproducible from its signed historical recipe.
	type file struct {
		Path string
		Mode uint32
		Data []byte
	}
	recovery := struct {
		Receipt  *InstanceResources
		Identity []byte
		Files    []file
	}{Receipt: receipt, Identity: raw}
	for _, resource := range receipt.Resources {
		if resource.Kind != "unit" && (resource.Kind != "file" || !applicationOwnedNativeFile(resource.LogicalName)) {
			continue
		}
		snapshot, err := captureHostFile(b.Manager.path(resource.Path))
		if err != nil || !snapshot.Exists || len(snapshot.Data) > 1<<20 {
			return errors.New("agent: original Senlun recovery configuration is unavailable")
		}
		recovery.Files = append(recovery.Files, file{resource.Path, uint32(snapshot.Mode.Perm()), snapshot.Data})
	}
	encoded, err := json.Marshal(recovery)
	if err != nil {
		return err
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "source.json", Mode: 0600, Size: int64(len(encoded)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := writer.Write(encoded); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	clear(encoded)
	backup, err := savePackageBackup(b.StateDirectory, task, RuntimeResource{Name: "senlun-v1-handover"}, receipt.PackageVersion, bytes.NewReader(archive.Bytes()))
	clear(archive.Bytes())
	if err != nil {
		return err
	}
	receipt.Backups = append(receipt.Backups, backup)
	if err := persist(); err != nil {
		return err
	}
	remove := b.sourceTask
	remove.Operation, remove.DeleteData = "uninstall", false
	if err := b.source.Remove(ctx, remove, receipt); err != nil {
		return err
	}
	receipt.RuntimeVersion = 2
	receipt.Resources = nil
	receipt.ConfigurationPaths = nil
	return persist()
}

func (b *senlunNativePackageBackend) Apply(ctx context.Context, task DeploymentTask, receipt *InstanceResources, persist func() error) error {
	if b.prepared == nil {
		return errors.New("agent: Senlun native installation was not prepared")
	}
	if b.permitsMigration(task, receipt) {
		if err := b.retainSource(ctx, task, receipt, persist); err != nil {
			return err
		}
	}
	if len(b.identity) == 0 {
		return b.NativeServicesPackageBackend.Apply(ctx, task, receipt, persist)
	}
	// Stage genuine owned resources without startup, import into the newly owned
	// state, then activate. No enrollment request or unowned service is stopped.
	install := task
	if b.source != nil {
		install.Operation = "install"
	}
	if err := installNativePackage(ctx, b.Manager, install, *b.prepared, receipt, persist, false); err != nil {
		return err
	}
	for _, state := range b.prepared.Plan.State {
		path := b.Manager.path(filepath.Join(state.Path, "agent-credentials.json"))
		if err := checkPackageParents(path); err != nil {
			return err
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return errors.New("agent: Senlun identity import refuses an existing destination")
		}
		if err := writeHostFileAtomic(path, b.identity, 0600); err != nil {
			return err
		}
		if err := b.Manager.run(ctx, "chown", state.User+":"+state.User, path); err != nil {
			return err
		}
	}
	clear(b.identity)
	b.identity = nil
	for _, unit := range b.prepared.Plan.Units {
		if !unit.Automatic {
			return errors.New("agent: Senlun collector must start automatically")
		}
		if err := b.Manager.run(ctx, "systemctl", "enable", unit.Name); err != nil {
			return err
		}
		if err := b.Manager.run(ctx, "systemctl", "start", unit.Name); err != nil {
			return err
		}
	}
	return nil
}

func (b *senlunNativePackageBackend) Healthy(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if err := b.NativeServicesPackageBackend.Healthy(ctx, task, receipt); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for _, state := range b.prepared.Plan.State {
			raw, err := readSenulunPackageCredentials(b.Manager.path(filepath.Join(state.Path, "agent-credentials.json")), b.serviceURL)
			if err != nil {
				return err
			}
			if len(raw) != 0 {
				id, err := parseSenlunNativeIdentity(raw, b.serviceURL)
				if err != nil || b.nodeID != "" && b.nodeID != id {
					return errors.New("agent: Senlun did not retain the expected monitoring identity")
				}
				for _, unit := range b.prepared.Plan.Units {
					resource := resourceNamed(receipt, "file", unit.LogicalName+":credential:enrollment-token")
					if resource != nil {
						if err := writeHostFileAtomic(b.Manager.path(resource.Path), nil, 0600); err != nil {
							return err
						}
						digest := sha256.Sum256(nil)
						resource.SHA256 = hex.EncodeToString(digest[:])
						if err := removeHostFile(b.Manager.path(filepath.Join("/run", unit.RuntimeDirectory, "enrollment-token"))); err != nil {
							return err
						}
					}
				}
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("agent: Senlun enrollment did not complete")
		case <-ticker.C:
		}
	}
}
