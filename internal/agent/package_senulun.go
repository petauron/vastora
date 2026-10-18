package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/senulun"
)

// Pulse contributes enrollment semantics only. File deployment, credentials,
// systemd confinement, backups and ownership remain in the generic executor.
func (b *SystemdPackageBackend) prepareSenulunInputs(task DeploymentTask, receipt *InstanceResources) (DeploymentTask, error) {
	if task.AppKey != senulun.AgentKey {
		return task, nil
	}
	var settings senulun.AgentConfig
	if json.Unmarshal(task.Config, &settings) != nil {
		return task, errors.New("agent: invalid Senulun configuration")
	}
	if err := settings.Validate(); err != nil {
		return task, err
	}
	release, err := os.ReadFile(b.Manager.path("/etc/os-release"))
	if err != nil || !senulunSupportedOS(release) {
		return task, errors.New("agent: Senulun requires Debian 12/13 or Ubuntu 24.04/26.04")
	}
	if environment := resourceNamed(receipt, "file", "environment"); environment != nil {
		raw, err := os.ReadFile(b.Manager.path(environment.Path))
		if err != nil {
			return task, err
		}
		settings, err = senulunRetainLocation(settings, raw)
		if err != nil {
			return task, err
		}
	}
	credentials, err := readSenulunPackageCredentials(b.Manager.path(senulunPackageIdentityPath(task, receipt)), settings.ServiceURL)
	if err != nil {
		return task, err
	}
	var secrets map[string]string
	if json.Unmarshal(task.Secrets, &secrets) != nil || secrets == nil {
		return task, errors.New("agent: invalid Senulun credentials")
	}
	if task.SenulunRestore != nil {
		if task.Operation != "install" || task.SenulunRestore.Validate() != nil {
			return task, errors.New("agent: invalid Senulun restoration task")
		}
		secrets["enrollment_token"] = ""
	} else if credentials == nil {
		if task.Operation != "install" {
			return task, errors.New("agent: retained Senulun identity is missing; explicit re-enrollment required")
		}
		token := secrets["enrollment_token"]
		if len(token) < 16 || len(token) > 512 || strings.ContainsAny(token, " \t\r\n") {
			return task, errors.New("agent: Senulun enrollment token is unavailable")
		}
	} else {
		secrets["enrollment_token"] = ""
	}
	// Retain every supplied key when merging host location overrides. The
	// runtime boundary separates Center metadata and rejects undeclared inputs;
	// preparation must not silently discard those inputs before validation.
	var runtimeConfig map[string]json.RawMessage
	if json.Unmarshal(task.Config, &runtimeConfig) != nil || runtimeConfig == nil {
		return task, errors.New("agent: invalid Pulse configuration")
	}
	if settings.NodeRegion != nil {
		runtimeConfig["node_region"], err = json.Marshal(*settings.NodeRegion)
		if err != nil {
			return task, err
		}
	}
	if settings.GeoIPProvider != nil {
		runtimeConfig["geoip_provider"], err = json.Marshal(*settings.GeoIPProvider)
		if err != nil {
			return task, err
		}
	}
	task.Config, err = json.Marshal(runtimeConfig)
	if err != nil {
		return task, err
	}
	task.Secrets, err = json.Marshal(secrets)
	return task, err
}

func senulunPackageIdentityPath(task DeploymentTask, receipt *InstanceResources) string {
	state := "/var/lib/" + packageIdentity(task.ApplicationID) + "-state"
	if resource := resourceNamed(receipt, "directory", "state"); resource != nil {
		state = resource.Path
	}
	return filepath.Join(state, "agent-credentials.json")
}

func readSenulunPackageCredentials(path, serviceURL string) ([]byte, error) {
	if err := checkPackageParents(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8192 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("agent: Senulun identity storage is not private")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var identity struct {
		ServiceURL      string `json:"service_url"`
		NodeID          string `json:"node_id"`
		AgentToken      string `json:"agent_token"`
		ProtocolVersion int    `json:"protocol_version"`
	}
	if json.Unmarshal(raw, &identity) != nil || identity.ServiceURL != serviceURL || identity.NodeID == "" || identity.AgentToken == "" || identity.ProtocolVersion != 2 {
		return nil, errors.New("agent: Senulun identity is invalid or belongs to another service")
	}
	return raw, nil
}

func (b *SystemdPackageBackend) completeSenulunEnrollment(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if task.AppKey != senulun.AgentKey {
		return nil
	}
	var settings senulun.AgentConfig
	if json.Unmarshal(task.Config, &settings) != nil {
		return errors.New("agent: invalid Senulun configuration")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		raw, err := readSenulunPackageCredentials(b.Manager.path(senulunPackageIdentityPath(task, receipt)), settings.ServiceURL)
		if err != nil {
			return err
		}
		if raw != nil {
			break
		}
		select {
		case <-ctx.Done():
			return errors.New("agent: Senulun enrollment did not complete")
		case <-ticker.C:
		}
	}
	source := resourceNamed(receipt, "file", "credential:enrollment-token")
	if source == nil {
		return errors.New("agent: Senulun enrollment source ownership missing")
	}
	if err := writeHostFileAtomic(b.Manager.path(source.Path), nil, 0600); err != nil {
		return err
	}
	digest := sha256.Sum256(nil)
	source.SHA256 = hex.EncodeToString(digest[:])
	// The service now uses its persisted identity, never the one-time token.
	return removeHostFile(b.Manager.path("/run/" + packageIdentity(task.ApplicationID) + "/enrollment-token"))
}

func (b *SystemdPackageBackend) restoreSenulunIdentity(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if task.SenulunRestore == nil {
		return nil
	}
	if task.AppKey != senulun.AgentKey || task.Operation != "install" || task.SenulunRestore.Validate() != nil {
		return errors.New("agent: invalid Senulun restoration task")
	}
	var settings senulun.AgentConfig
	if json.Unmarshal(task.Config, &settings) != nil || settings.Validate() != nil {
		return errors.New("agent: invalid Senulun restoration configuration")
	}
	binary := resourceNamed(receipt, "file", "artifact:"+task.Manifest.Runtime.Systemd.Executable)
	if binary == nil {
		return errors.New("agent: Senulun executable receipt missing")
	}
	path := b.Manager.path(senulunPackageIdentityPath(task, receipt))
	if err := checkPackageParents(path); err != nil {
		return err
	}
	if entries, err := os.ReadDir(filepath.Dir(path)); err == nil {
		if len(entries) != 0 {
			return errors.New("agent: Senulun restoration cannot overwrite existing state")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		return err
	}
	user, _, _ := strings.Cut(task.Manifest.Runtime.Systemd.User, ":")
	if user == "" || user == "root" {
		return errors.New("agent: Senulun restoration requires a dedicated service user")
	}
	if err := b.Manager.run(ctx, "chown", user, filepath.Dir(path)); err != nil {
		return errors.New("agent: Senulun identity ownership could not be set")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// The executable comes from the verified installed recipe, not a command
	// fallback. Retained historical versions still speak their original CLI.
	prefix := "SENULUN_"
	if task.Manifest.Runtime.Systemd.Executable == "pulse-agent" {
		prefix = "PULSE_"
	}
	args := []string{"-u", user, "--", "env", "-i", prefix + "SERVICE_URL=" + settings.ServiceURL, prefix + "CREDENTIALS_PATH=" + path, b.Manager.path(binary.Path), "credentials", "import", task.SenulunRestore.NodeID}
	input := strings.NewReader(task.SenulunRestore.Token + "\n")
	var err error
	if b.Manager.RunInputCommand != nil {
		err = b.Manager.RunInputCommand(ctx, input, "runuser", args...)
	} else {
		cmd := exec.CommandContext(ctx, "runuser", args...)
		cmd.Stdin = input
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		err = cmd.Run()
	}
	if err != nil {
		return errors.New("agent: Senulun identity import requires inspection")
	}
	raw, err := readSenulunPackageCredentials(path, settings.ServiceURL)
	var saved struct {
		NodeID string `json:"node_id"`
		Token  string `json:"agent_token"`
	}
	if err != nil || json.Unmarshal(raw, &saved) != nil || saved.NodeID != task.SenulunRestore.NodeID || saved.Token != task.SenulunRestore.Token {
		return errors.New("agent: Senulun importer did not retain approved identity")
	}
	return nil
}
