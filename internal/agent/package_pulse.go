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

	"github.com/petauron/vastora/internal/pulse"
)

// Pulse contributes enrollment semantics only. File deployment, credentials,
// systemd confinement, backups and ownership remain in the generic executor.
func (b *SystemdPackageBackend) preparePulseInputs(task DeploymentTask, receipt *InstanceResources) (DeploymentTask, error) {
	if task.AppKey != pulse.AgentKey {
		return task, nil
	}
	var settings pulse.AgentConfig
	if json.Unmarshal(task.Config, &settings) != nil {
		return task, errors.New("agent: invalid Pulse configuration")
	}
	if err := settings.Validate(); err != nil {
		return task, err
	}
	release, err := os.ReadFile(b.Manager.path("/etc/os-release"))
	if err != nil || !pulseSupportedOS(release) {
		return task, errors.New("agent: Pulse requires Debian 12/13 or Ubuntu 24.04/26.04")
	}
	if environment := resourceNamed(receipt, "file", "environment"); environment != nil {
		raw, err := os.ReadFile(b.Manager.path(environment.Path))
		if err != nil {
			return task, err
		}
		settings, err = pulseRetainLocation(settings, raw)
		if err != nil {
			return task, err
		}
	}
	credentials, err := readPulsePackageCredentials(b.Manager.path(pulsePackageIdentityPath(task, receipt)), settings.ServiceURL)
	if err != nil {
		return task, err
	}
	var secrets map[string]string
	if json.Unmarshal(task.Secrets, &secrets) != nil || secrets == nil {
		return task, errors.New("agent: invalid Pulse credentials")
	}
	if task.PulseRestore != nil {
		if task.Operation != "install" || task.PulseRestore.Validate() != nil {
			return task, errors.New("agent: invalid Pulse restoration task")
		}
		secrets["enrollment_token"] = ""
	} else if credentials == nil {
		if task.Operation != "install" {
			return task, errors.New("agent: retained Pulse identity is missing; explicit re-enrollment required")
		}
		token := secrets["enrollment_token"]
		if len(token) < 16 || len(token) > 512 || strings.ContainsAny(token, " \t\r\n") {
			return task, errors.New("agent: Pulse enrollment token is unavailable")
		}
	} else {
		secrets["enrollment_token"] = ""
	}
	task.Config, err = json.Marshal(settings)
	if err != nil {
		return task, err
	}
	task.Secrets, err = json.Marshal(secrets)
	return task, err
}

func pulsePackageIdentityPath(task DeploymentTask, receipt *InstanceResources) string {
	state := "/var/lib/" + packageIdentity(task.ApplicationID) + "-state"
	if resource := resourceNamed(receipt, "directory", "state"); resource != nil {
		state = resource.Path
	}
	return filepath.Join(state, "agent-credentials.json")
}

func readPulsePackageCredentials(path, serviceURL string) ([]byte, error) {
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
		return nil, errors.New("agent: Pulse identity storage is not private")
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
		return nil, errors.New("agent: Pulse identity is invalid or belongs to another service")
	}
	return raw, nil
}

func (b *SystemdPackageBackend) completePulseEnrollment(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if task.AppKey != pulse.AgentKey {
		return nil
	}
	var settings pulse.AgentConfig
	if json.Unmarshal(task.Config, &settings) != nil {
		return errors.New("agent: invalid Pulse configuration")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		raw, err := readPulsePackageCredentials(b.Manager.path(pulsePackageIdentityPath(task, receipt)), settings.ServiceURL)
		if err != nil {
			return err
		}
		if raw != nil {
			break
		}
		select {
		case <-ctx.Done():
			return errors.New("agent: Pulse enrollment did not complete")
		case <-ticker.C:
		}
	}
	source := resourceNamed(receipt, "file", "credential:enrollment-token")
	if source == nil {
		return errors.New("agent: Pulse enrollment source ownership missing")
	}
	if err := writeHostFileAtomic(b.Manager.path(source.Path), nil, 0600); err != nil {
		return err
	}
	digest := sha256.Sum256(nil)
	source.SHA256 = hex.EncodeToString(digest[:])
	// The service now uses its persisted identity, never the one-time token.
	return removeHostFile(b.Manager.path("/run/" + packageIdentity(task.ApplicationID) + "/enrollment-token"))
}

func (b *SystemdPackageBackend) restorePulseIdentity(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if task.PulseRestore == nil {
		return nil
	}
	if task.AppKey != pulse.AgentKey || task.Operation != "install" || task.PulseRestore.Validate() != nil {
		return errors.New("agent: invalid Pulse restoration task")
	}
	var settings pulse.AgentConfig
	if json.Unmarshal(task.Config, &settings) != nil || settings.Validate() != nil {
		return errors.New("agent: invalid Pulse restoration configuration")
	}
	binary := resourceNamed(receipt, "file", "artifact:"+task.Manifest.Runtime.Systemd.Executable)
	if binary == nil {
		return errors.New("agent: Pulse executable receipt missing")
	}
	path := b.Manager.path(pulsePackageIdentityPath(task, receipt))
	if err := checkPackageParents(path); err != nil {
		return err
	}
	if entries, err := os.ReadDir(filepath.Dir(path)); err == nil {
		if len(entries) != 0 {
			return errors.New("agent: Pulse restoration cannot overwrite existing state")
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
		return errors.New("agent: Pulse restoration requires a dedicated service user")
	}
	if err := b.Manager.run(ctx, "chown", user, filepath.Dir(path)); err != nil {
		return errors.New("agent: Pulse identity ownership could not be set")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{"-u", user, "--", "env", "-i", "PULSE_SERVICE_URL=" + settings.ServiceURL, "PULSE_CREDENTIALS_PATH=" + path, b.Manager.path(binary.Path), "credentials", "import", task.PulseRestore.NodeID}
	input := strings.NewReader(task.PulseRestore.Token + "\n")
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
		return errors.New("agent: Pulse identity import requires inspection")
	}
	raw, err := readPulsePackageCredentials(path, settings.ServiceURL)
	var saved struct {
		NodeID string `json:"node_id"`
		Token  string `json:"agent_token"`
	}
	if err != nil || json.Unmarshal(raw, &saved) != nil || saved.NodeID != task.PulseRestore.NodeID || saved.Token != task.PulseRestore.Token {
		return errors.New("agent: Pulse importer did not retain approved identity")
	}
	return nil
}
