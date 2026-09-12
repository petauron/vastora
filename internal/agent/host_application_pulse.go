package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/platform"
	"github.com/petauron/vastora/internal/pulse"
)

const (
	pulseBinary          = "/opt/vastora/pulse-agent/pulse-agent"
	pulseDigest          = "/opt/vastora/pulse-agent/package.json"
	pulseArchive         = "/opt/vastora/pulse-agent/release.tar.gz"
	pulseEnv             = "/etc/vastora/pulse-agent.env"
	pulseToken           = "/etc/vastora/pulse-enrollment-token"
	pulseRuntimeToken    = "/run/vastora-pulse-agent/enrollment-token"
	pulseUnitPath        = "/etc/systemd/system/vastora-pulse-agent.service"
	pulseUnitName        = "vastora-pulse-agent.service"
	pulseState           = "/var/lib/vastora-pulse-agent"
	pulseCredentialsPath = pulseState + "/agent-credentials.json"
	pulseUser            = "vastora-pulse"
)

type pulsePackageProof struct {
	Version       string `json:"version"`
	ArchiveSHA256 string `json:"archiveSHA256"`
	BinarySHA256  string `json:"binarySHA256"`
}

// Read only the exact regular binary from the signed, checksum-verified
// archive. Never extract paths, links, permissions or scripts supplied by tar.
func pulseArchiveBinary(archive []byte, version, architecture string) ([]byte, error) {
	arch := map[string]string{platform.AMD64: "x86_64", platform.ARM64: "aarch64"}[architecture]
	if arch == "" {
		return nil, errors.New("agent: unsupported Pulse architecture")
	}
	wanted := "pulse-v" + version + "-linux-" + arch + "/pulse-agent"
	zipped, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, errors.New("agent: invalid Pulse archive")
	}
	defer zipped.Close()
	reader := tar.NewReader(io.LimitReader(zipped, 128<<20))
	var binary []byte
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("agent: invalid Pulse archive")
		}
		if header.Name != wanted {
			continue
		}
		if binary != nil || header.Typeflag != tar.TypeReg || header.Size < 4 || header.Size > maxArtifactBytes {
			return nil, errors.New("agent: invalid Pulse executable entry")
		}
		binary, err = io.ReadAll(io.LimitReader(reader, maxArtifactBytes+1))
		if err != nil || int64(len(binary)) != header.Size || !bytes.HasPrefix(binary, []byte("\x7fELF")) {
			return nil, errors.New("agent: invalid Pulse executable")
		}
	}
	if binary == nil {
		return nil, errors.New("agent: Pulse executable is missing from archive")
	}
	return binary, nil
}

func (manager SystemdHostApplicationManager) pulseTarget() (platform.Target, error) {
	target := manager.HostTarget
	if target.OS == "" && target.Architecture == "" {
		return platform.Parse(runtime.GOOS, runtime.GOARCH)
	}
	return platform.Parse(target.OS, target.Architecture)
}

func pulseSupportedOS(raw []byte) bool {
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = strings.Trim(value, "\"'")
		}
	}
	return values["ID"] == "debian" && (values["VERSION_ID"] == "12" || values["VERSION_ID"] == "13") || values["ID"] == "ubuntu" && (values["VERSION_ID"] == "24.04" || values["VERSION_ID"] == "26.04")
}

func pulseEnvironment(config pulse.AgentConfig) []byte {
	return []byte("PULSE_SERVICE_URL=" + strconv.Quote(config.ServiceURL) + "\nPULSE_NODE_NAME=" + strconv.Quote(config.NodeName) + "\nPULSE_NODE_GROUP=" + strconv.Quote(config.NodeGroup) + "\nPULSE_CREDENTIALS_PATH=" + pulseCredentialsPath + "\nPULSE_INTERVAL_SECONDS=30\n")
}

func pulseUnit(applicationID string) []byte {
	// systemd credentials may use ACLs with group mode bits set. Pulse requires
	// owner-only mode bits, so give it a private runtime copy without weakening
	// its validation or exposing the token in the environment or command line.
	return []byte("# Managed by Vastora\n# Application: " + applicationID + `
[Unit]
Description=Pulse host monitoring
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
User=vastora-pulse
Group=vastora-pulse
EnvironmentFile=/etc/vastora/pulse-agent.env
LoadCredential=pulse-enrollment:/etc/vastora/pulse-enrollment-token
RuntimeDirectory=vastora-pulse-agent
RuntimeDirectoryMode=0700
ExecStartPre=/usr/bin/install -m 0600 %d/pulse-enrollment /run/vastora-pulse-agent/enrollment-token
Environment=PULSE_ENROLLMENT_TOKEN_FILE=/run/vastora-pulse-agent/enrollment-token
ExecStart=/opt/vastora/pulse-agent/pulse-agent
Restart=on-failure
RestartSec=10s
StateDirectory=vastora-pulse-agent
StateDirectoryMode=0700
UMask=0077
NoNewPrivileges=yes
PrivateDevices=yes
PrivateTmp=yes
ProtectHome=yes
ProtectSystem=strict
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6
RestrictSUIDSGID=yes
LockPersonality=yes
CapabilityBoundingSet=
AmbientCapabilities=
ReadWritePaths=/var/lib/vastora-pulse-agent

[Install]
WantedBy=multi-user.target
`)
}

// Ownership is independent of the unit template, which may change on upgrade.
// RestorePulse still checks the full current unit before starting retained files.
func pulseUnitOwnedBy(unit []byte, applicationID string) bool {
	return applicationID != "" && !strings.ContainsAny(applicationID, "\r\n") &&
		bytes.HasPrefix(unit, []byte("# Managed by Vastora\n# Application: "+applicationID+"\n"))
}

func (manager SystemdHostApplicationManager) ApplyPulse(ctx context.Context, task DeploymentTask) (ApplicationTaskResult, error) {
	var config pulse.AgentConfig
	if json.Unmarshal(task.Config, &config) != nil {
		return ApplicationTaskResult{}, errors.New("agent: invalid Pulse configuration")
	}
	if err := config.Validate(); err != nil {
		return ApplicationTaskResult{}, err
	}
	osRelease, err := os.ReadFile(manager.path("/etc/os-release"))
	if err != nil || !pulseSupportedOS(osRelease) {
		return ApplicationTaskResult{}, errors.New("agent: this Pulse package requires Debian 12/13 or Ubuntu 24.04/26.04")
	}
	target, err := manager.pulseTarget()
	if err != nil {
		return ApplicationTaskResult{}, err
	}
	artifact, err := declaredArtifact(task.Manifest, "pulse-agent", target)
	if err != nil {
		return ApplicationTaskResult{}, err
	}
	archive, err := manager.downloadArtifact(ctx, artifact)
	if err != nil {
		return ApplicationTaskResult{}, err
	}
	binary, err := pulseArchiveBinary(archive, task.Manifest.Version, target.Architecture)
	if err != nil {
		return ApplicationTaskResult{}, err
	}
	if err := verifyArtifactELF(binary, target.Architecture); err != nil {
		return ApplicationTaskResult{}, err
	}
	paths := []string{pulseBinary, pulseEnv, pulseUnitPath, pulseDigest, pulseToken, pulseArchive}
	snapshots := make([]hostFileSnapshot, 0, len(paths))
	for _, path := range paths {
		snapshot, err := captureHostFile(manager.path(path))
		if err != nil {
			return ApplicationTaskResult{}, err
		}
		snapshots = append(snapshots, snapshot)
	}
	unit := pulseUnit(task.ApplicationID)
	if snapshots[2].Exists && !pulseUnitOwnedBy(snapshots[2].Data, task.ApplicationID) {
		return ApplicationTaskResult{}, errors.New("agent: Pulse service is not owned by this application")
	}
	if !snapshots[2].Exists && (snapshots[0].Exists || snapshots[1].Exists || snapshots[3].Exists || snapshots[4].Exists || snapshots[5].Exists) {
		return ApplicationTaskResult{}, errors.New("agent: refusing to overwrite unmanaged Pulse files")
	}
	credentials, err := manager.pulseCredentials(config.ServiceURL)
	if err != nil {
		return ApplicationTaskResult{}, err
	}
	if credentials == nil && task.Operation != "install" {
		return ApplicationTaskResult{}, errors.New("agent: Pulse identity is missing; reinstall the collector to enroll again")
	}
	var secrets struct {
		Token string `json:"enrollment_token"`
	}
	_ = json.Unmarshal(task.Secrets, &secrets)
	if credentials == nil && (len(secrets.Token) < 16 || len(secrets.Token) > 512 || strings.ContainsAny(secrets.Token, " \t\r\n")) {
		return ApplicationTaskResult{}, errors.New("agent: Pulse enrollment is unavailable; retry installation")
	}
	if manager.run(ctx, "getent", "passwd", pulseUser) != nil {
		if err := manager.run(ctx, "useradd", "--system", "--user-group", "--home-dir", pulseState, "--no-create-home", "--shell", "/usr/sbin/nologin", pulseUser); err != nil {
			return ApplicationTaskResult{}, errors.New("agent: could not create Pulse service account")
		}
	}
	digest := sha256.Sum256(binary)
	proof, _ := json.Marshal(pulsePackageProof{task.Manifest.Version, artifact.SHA256, hex.EncodeToString(digest[:])})
	token := []byte(secrets.Token)
	if credentials != nil {
		token = []byte{}
	}
	// Retain the upstream archive, including its license and third-party notices.
	contents := [][]byte{binary, pulseEnvironment(config), unit, proof, token, archive}
	modes := []os.FileMode{0o755, 0o600, 0o644, 0o600, 0o600, 0o644}
	enableAttempted, restartAttempted := false, false
	for index, path := range paths {
		if err = writeHostFileAtomic(manager.path(path), contents[index], modes[index]); err != nil {
			break
		}
	}
	if err == nil {
		for _, args := range [][]string{{"daemon-reload"}, {"enable", pulseUnitName}, {"restart", pulseUnitName}} {
			if args[0] == "enable" {
				enableAttempted = true
			}
			if args[0] == "restart" {
				restartAttempted = true
			}
			if err = manager.run(ctx, "systemctl", args...); err != nil {
				break
			}
		}
	}
	if err == nil {
		err = manager.waitPulseEnrollment(ctx, config.ServiceURL)
	}
	if err != nil {
		// Stop before restoring files. Keep any newly issued identity: a token
		// cannot safely be used twice after successful enrollment.
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if restartAttempted {
			if stopErr := manager.run(rollbackCtx, "systemctl", "stop", pulseUnitName); stopErr != nil {
				return ApplicationTaskResult{}, errors.New("agent: Pulse installation failed; service could not be stopped")
			}
		}
		var rollbackErr error
		if enableAttempted && !snapshots[2].Exists {
			rollbackErr = manager.run(rollbackCtx, "systemctl", "disable", pulseUnitName)
		}
		rollbackErr = errors.Join(rollbackErr, restoreHostFiles(snapshots))
		rollbackErr = errors.Join(rollbackErr, manager.run(rollbackCtx, "systemctl", "daemon-reload"))
		if snapshots[2].Exists && restartAttempted && rollbackErr == nil {
			rollbackErr = errors.Join(rollbackErr, manager.run(rollbackCtx, "systemctl", "restart", pulseUnitName))
		}
		return ApplicationTaskResult{}, errors.Join(fmt.Errorf("agent: Pulse installation failed: %w", err), rollbackErr)
	}
	// Leave an empty credential source so reboot works without retaining the
	// one-time token. Existing agent credentials are owned by Pulse itself.
	if err := writeHostFileAtomic(manager.path(pulseToken), []byte{}, 0o600); err != nil {
		return ApplicationTaskResult{}, err
	}
	if err := removeHostFile(manager.path(pulseRuntimeToken)); err != nil {
		return ApplicationTaskResult{}, err
	}
	return ApplicationTaskResult{}, nil
}

func (manager SystemdHostApplicationManager) pulseCredentials(serviceURL string) ([]byte, error) {
	info, err := os.Lstat(manager.path(pulseCredentialsPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8192 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("agent: Pulse credential storage is not private")
	}
	snapshot, err := captureHostFile(manager.path(pulseCredentialsPath))
	if err != nil || !snapshot.Exists {
		return nil, err
	}
	var value struct {
		ServiceURL      string `json:"service_url"`
		NodeID          string `json:"node_id"`
		AgentToken      string `json:"agent_token"`
		ProtocolVersion int    `json:"protocol_version"`
	}
	if len(snapshot.Data) > 8192 || json.Unmarshal(snapshot.Data, &value) != nil || value.ServiceURL != serviceURL || value.NodeID == "" || value.AgentToken == "" || value.ProtocolVersion != 2 {
		return nil, errors.New("agent: retained Pulse identity is invalid or belongs to another monitoring service")
	}
	return snapshot.Data, nil
}

func (manager SystemdHostApplicationManager) waitPulseEnrollment(ctx context.Context, serviceURL string) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		credentials, err := manager.pulseCredentials(serviceURL)
		if err != nil {
			return err
		}
		if credentials != nil {
			return manager.run(ctx, "systemctl", "is-active", "--quiet", pulseUnitName)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (manager SystemdHostApplicationManager) RestorePulse(ctx context.Context, task DeploymentTask) error {
	var config pulse.AgentConfig
	if json.Unmarshal(task.Config, &config) != nil || config.Validate() != nil {
		return errors.New("agent: invalid retained Pulse configuration")
	}
	target, err := manager.pulseTarget()
	if err != nil {
		return err
	}
	artifact, err := declaredArtifact(task.Manifest, "pulse-agent", target)
	if err != nil {
		return err
	}
	contents := map[string][]byte{}
	for _, path := range []string{pulseBinary, pulseDigest, pulseEnv, pulseUnitPath} {
		file, err := captureHostFile(manager.path(path))
		if err != nil || !file.Exists {
			return errors.New("agent: retained Pulse installation is incomplete")
		}
		contents[path] = file.Data
	}
	var proof pulsePackageProof
	digest := sha256.Sum256(contents[pulseBinary])
	if json.Unmarshal(contents[pulseDigest], &proof) != nil || proof.Version != task.Manifest.Version || proof.ArchiveSHA256 != artifact.SHA256 || proof.BinarySHA256 != hex.EncodeToString(digest[:]) || !bytes.Equal(contents[pulseEnv], pulseEnvironment(config)) || !bytes.Equal(contents[pulseUnitPath], pulseUnit(task.ApplicationID)) {
		return errors.New("agent: retained Pulse installation was modified")
	}
	credentials, err := manager.pulseCredentials(config.ServiceURL)
	if err != nil || credentials == nil {
		return errors.New("agent: retained Pulse identity is unavailable")
	}
	if err := writeHostFileAtomic(manager.path(pulseToken), []byte{}, 0o600); err != nil {
		return err
	}
	if manager.run(ctx, "systemctl", "is-active", "--quiet", pulseUnitName) == nil {
		return nil
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", pulseUnitName}, {"start", pulseUnitName}} {
		if err := manager.run(ctx, "systemctl", args...); err != nil {
			return err
		}
	}
	return manager.run(ctx, "systemctl", "is-active", "--quiet", pulseUnitName)
}

func (manager SystemdHostApplicationManager) RemovePulse(ctx context.Context, applicationID string, deleteData bool) error {
	unit, err := captureHostFile(manager.path(pulseUnitPath))
	if err != nil {
		return err
	}
	if !unit.Exists {
		return nil
	}
	if !pulseUnitOwnedBy(unit.Data, applicationID) {
		return errors.New("agent: refusing to remove an unmanaged Pulse service")
	}
	if err := manager.run(ctx, "systemctl", "disable", "--now", pulseUnitName); err != nil {
		return err
	}
	// No recursive removal: delete only the managed files, and preserve node
	// identity unless the user explicitly selected delete-data uninstall.
	paths := []string{pulseBinary, pulseDigest, pulseEnv, pulseToken, pulseArchive}
	if deleteData {
		paths = append(paths, pulseCredentialsPath)
	}
	for _, path := range paths {
		if err := removeHostFile(manager.path(path)); err != nil {
			return fmt.Errorf("agent: remove Pulse file %s: %w", filepath.Base(path), err)
		}
	}
	if err := removeHostFile(manager.path(pulseUnitPath)); err != nil {
		return err
	}
	return manager.run(ctx, "systemctl", "daemon-reload")
}
