package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/platform"
)

const (
	komariBinaryPath         = "/opt/komari/agent"
	komariConfigPath         = "/etc/komari-agent/config.json"
	komariUnitPath           = "/etc/systemd/system/komari-agent.service"
	komariRemovalJournalPath = "/var/lib/vastora/komari-uninstall.json"
	maxArtifactBytes         = 64 << 20
	komariUnitMarker         = "# Managed by Vastora\n"
)

type SystemdHostApplicationManager struct {
	RootDir    string
	HTTPClient *http.Client
	RunCommand func(context.Context, string, ...string) error
	HostTarget platform.Target
}

type komariConfig struct {
	Endpoint           string  `json:"endpoint"`
	Token              string  `json:"token"`
	Interval           float64 `json:"interval"`
	InfoReportInterval int     `json:"info_report_interval"`
	DisableAutoUpdate  bool    `json:"disable_auto_update"`
	DisableWebSSH      bool    `json:"disable_web_ssh"`
	IgnoreUnsafeCert   bool    `json:"ignore_unsafe_cert"`
	ProtocolVersion    int     `json:"protocol_version"`
}

type komariRemovalJournal struct {
	Version int                          `json:"version"`
	Files   map[string]komariRemovalFile `json:"files"`
}

type komariRemovalFile struct {
	Exists bool   `json:"exists"`
	SHA256 string `json:"sha256,omitempty"`
}

func (manager SystemdHostApplicationManager) ApplyKomari(ctx context.Context, task DeploymentTask) error {
	expectedVersion, official := OfficialAppVersion("komari-agent")
	if task.Manifest.ID != "komari-agent" || !official || task.Manifest.Version != expectedVersion {
		return errors.New("agent: unsupported Komari Agent package")
	}
	var input struct {
		Endpoint string `json:"endpoint"`
	}
	var secretInput struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(task.Config, &input) != nil || json.Unmarshal(task.Secrets, &secretInput) != nil {
		return errors.New("agent: invalid Komari Agent configuration")
	}
	endpoint, err := normalizedKomariEndpoint(input.Endpoint)
	if err != nil || strings.TrimSpace(secretInput.Token) == "" || len(secretInput.Token) > 4096 {
		return errors.New("agent: incomplete Komari Agent configuration")
	}
	target := manager.HostTarget
	if target.OS == "" && target.Architecture == "" {
		target, err = platform.Parse(runtime.GOOS, runtime.GOARCH)
	} else {
		target, err = platform.Parse(target.OS, target.Architecture)
	}
	if err != nil {
		return fmt.Errorf("agent: install Komari Agent: %w", err)
	}
	artifact, err := declaredArtifact(task.Manifest, "komari-agent", target)
	if err != nil {
		return err
	}
	binary, err := manager.downloadArtifact(ctx, artifact)
	if err != nil {
		return err
	}
	config, err := json.MarshalIndent(komariConfig{
		Endpoint: endpoint, Token: strings.TrimSpace(secretInput.Token), Interval: 3,
		InfoReportInterval: 5, DisableAutoUpdate: true, DisableWebSSH: true,
		IgnoreUnsafeCert: false, ProtocolVersion: 2,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("agent: encode Komari Agent configuration: %w", err)
	}
	config = append(config, '\n')
	unit := komariUnit()
	paths := []string{manager.path(komariBinaryPath), manager.path(komariConfigPath), manager.path(komariUnitPath)}
	snapshots := make([]hostFileSnapshot, 0, len(paths))
	for _, path := range paths {
		snapshot, captureErr := captureHostFile(path)
		if captureErr != nil {
			return captureErr
		}
		snapshots = append(snapshots, snapshot)
	}
	if snapshots[2].Exists && !bytes.Contains(snapshots[2].Data, []byte(komariUnitMarker)) {
		return errors.New("agent: refusing to replace a Komari Agent service not managed by Vastora")
	}
	if !snapshots[2].Exists && (snapshots[0].Exists || snapshots[1].Exists) {
		return errors.New("agent: refusing to replace Komari Agent files not managed by Vastora")
	}
	err = writeHostFileAtomic(paths[0], binary, 0o755)
	if err == nil {
		err = writeHostFileAtomic(paths[1], config, 0o600)
		if err == nil {
			err = writeHostFileAtomic(paths[2], unit, 0o644)
		}
	}
	if err == nil {
		err = manager.run(ctx, "systemctl", "daemon-reload")
	}
	if err == nil {
		err = manager.run(ctx, "systemctl", "enable", "komari-agent.service")
	}
	if err == nil {
		err = manager.run(ctx, "systemctl", "restart", "komari-agent.service")
	}
	if err == nil {
		err = manager.run(ctx, "systemctl", "is-active", "--quiet", "komari-agent.service")
	}
	if err == nil {
		return nil
	}
	rollbackErr := restoreHostFiles(snapshots)
	rollbackErr = errors.Join(rollbackErr, manager.run(ctx, "systemctl", "daemon-reload"))
	if snapshots[2].Exists {
		rollbackErr = errors.Join(rollbackErr, manager.run(ctx, "systemctl", "enable", "komari-agent.service"))
		rollbackErr = errors.Join(rollbackErr, manager.run(ctx, "systemctl", "restart", "komari-agent.service"))
	} else {
		_ = manager.run(ctx, "systemctl", "disable", "--now", "komari-agent.service")
	}
	return fmt.Errorf("agent: apply Komari Agent: %w", errors.Join(err, rollbackErr))
}

func komariUnit() []byte {
	return []byte(komariUnitMarker + `[Unit]
Description=Komari Agent
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
ExecStart=/opt/komari/agent --config /etc/komari-agent/config.json
WorkingDirectory=/opt/komari
Restart=always
RestartSec=5
User=root

[Install]
WantedBy=multi-user.target
`)
}

// RestoreKomari restarts only the Vastora-managed binary and configuration
// whose bytes still match the last successful signed manifest. It deliberately
// has no download path so Center outages cannot introduce new code.
func (manager SystemdHostApplicationManager) RestoreKomari(ctx context.Context, task DeploymentTask) error {
	expectedVersion, official := OfficialAppVersion(task.Manifest.ID)
	if task.Manifest.ID != "komari-agent" || !official || task.Manifest.Version != expectedVersion || !strings.HasSuffix(task.AppKey, "/"+task.Manifest.ID) {
		return errors.New("agent: invalid Komari Agent restore state")
	}
	var input struct {
		Endpoint string `json:"endpoint"`
	}
	var secretInput struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(task.Config, &input) != nil || json.Unmarshal(task.Secrets, &secretInput) != nil {
		return errors.New("agent: invalid Komari Agent restore configuration")
	}
	endpoint, err := normalizedKomariEndpoint(input.Endpoint)
	if err != nil || strings.TrimSpace(secretInput.Token) == "" || len(secretInput.Token) > 4096 {
		return errors.New("agent: incomplete Komari Agent restore configuration")
	}
	target := manager.HostTarget
	if target.OS == "" && target.Architecture == "" {
		target, err = platform.Parse(runtime.GOOS, runtime.GOARCH)
	} else {
		target, err = platform.Parse(target.OS, target.Architecture)
	}
	if err != nil {
		return fmt.Errorf("agent: restore Komari Agent: %w", err)
	}
	artifact, err := declaredArtifact(task.Manifest, "komari-agent", target)
	if err != nil {
		return err
	}
	desiredConfig, err := json.MarshalIndent(komariConfig{
		Endpoint: endpoint, Token: strings.TrimSpace(secretInput.Token), Interval: 3,
		InfoReportInterval: 5, DisableAutoUpdate: true, DisableWebSSH: true,
		IgnoreUnsafeCert: false, ProtocolVersion: 2,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("agent: encode Komari Agent restore configuration: %w", err)
	}
	desiredConfig = append(desiredConfig, '\n')
	binary, err := captureHostFile(manager.path(komariBinaryPath))
	if err != nil || !binary.Exists {
		return errors.Join(errors.New("agent: managed Komari Agent binary is unavailable"), err)
	}
	digest := sha256.Sum256(binary.Data)
	if hex.EncodeToString(digest[:]) != artifact.SHA256 {
		return errors.New("agent: managed Komari Agent binary no longer matches its signed manifest")
	}
	unit, err := captureHostFile(manager.path(komariUnitPath))
	if err != nil || !unit.Exists || !bytes.Equal(unit.Data, komariUnit()) {
		return errors.Join(errors.New("agent: managed Komari Agent service is unavailable"), err)
	}
	config, err := captureHostFile(manager.path(komariConfigPath))
	if err != nil {
		return err
	}
	configRestored := !config.Exists || !bytes.Equal(config.Data, desiredConfig)
	if configRestored {
		if err := writeHostFileAtomic(manager.path(komariConfigPath), desiredConfig, 0o600); err != nil {
			return fmt.Errorf("agent: restore last-known-good Komari Agent configuration: %w", err)
		}
	}
	if !configRestored && manager.run(ctx, "systemctl", "is-active", "--quiet", "komari-agent.service") == nil {
		return nil
	}
	for _, command := range [][]string{{"daemon-reload"}, {"enable", "komari-agent.service"}, {"restart", "komari-agent.service"}, {"is-active", "--quiet", "komari-agent.service"}} {
		if err := manager.run(ctx, "systemctl", command...); err != nil {
			return fmt.Errorf("agent: restore Komari Agent service: %w", err)
		}
	}
	return nil
}

func (manager SystemdHostApplicationManager) RemoveKomari(ctx context.Context) error {
	journal, err := manager.prepareKomariRemoval()
	if err != nil || journal == nil {
		return err
	}
	unitPath := manager.path(komariUnitPath)
	paths := []string{unitPath, manager.path(komariConfigPath), manager.path(komariBinaryPath)}
	unitExists := false
	// A resumed uninstall may encounter a service or file replaced by the
	// operator. Prove ownership of every remaining file before stopping it.
	for _, path := range paths {
		snapshot, err := validateJournaledKomariFile(path, journal)
		if err != nil {
			return err
		}
		if path == unitPath {
			unitExists = snapshot.Exists
		}
	}
	if unitExists {
		if err := manager.run(ctx, "systemctl", "disable", "--now", "komari-agent.service"); err != nil {
			return fmt.Errorf("agent: stop Komari Agent: %w", err)
		}
	}
	for _, path := range paths {
		if err := removeJournaledKomariFile(path, journal); err != nil {
			return err
		}
	}
	if err := manager.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("agent: reload systemd after removing Komari Agent: %w", err)
	}
	return removeHostFile(manager.path(komariRemovalJournalPath))
}

func (manager SystemdHostApplicationManager) prepareKomariRemoval() (*komariRemovalJournal, error) {
	journalPath := manager.path(komariRemovalJournalPath)
	snapshot, err := captureHostFile(journalPath)
	if err != nil {
		return nil, fmt.Errorf("agent: read Komari Agent uninstall journal: %w", err)
	}
	if snapshot.Exists {
		if snapshot.Mode&0o077 != 0 {
			return nil, errors.New("agent: Komari Agent uninstall journal is not protected")
		}
		var journal komariRemovalJournal
		if json.Unmarshal(snapshot.Data, &journal) != nil || journal.Version != 1 || len(journal.Files) != 3 {
			return nil, errors.New("agent: invalid Komari Agent uninstall journal")
		}
		return &journal, nil
	}
	paths := []string{manager.path(komariUnitPath), manager.path(komariConfigPath), manager.path(komariBinaryPath)}
	snapshots := make([]hostFileSnapshot, 0, len(paths))
	for _, path := range paths {
		snapshot, err := captureHostFile(path)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	if !snapshots[0].Exists {
		if !snapshots[1].Exists && !snapshots[2].Exists {
			return nil, nil
		}
		return nil, errors.New("agent: refusing to remove residual Komari Agent files without Vastora ownership evidence")
	}
	if !bytes.Contains(snapshots[0].Data, []byte(komariUnitMarker)) {
		return nil, errors.New("agent: refusing to remove a Komari Agent service not managed by Vastora")
	}
	journal := &komariRemovalJournal{Version: 1, Files: make(map[string]komariRemovalFile, len(snapshots))}
	for _, snapshot := range snapshots {
		entry := komariRemovalFile{Exists: snapshot.Exists}
		if snapshot.Exists {
			digest := sha256.Sum256(snapshot.Data)
			entry.SHA256 = hex.EncodeToString(digest[:])
		}
		journal.Files[snapshot.Path] = entry
	}
	raw, err := json.Marshal(journal)
	if err != nil {
		return nil, fmt.Errorf("agent: encode Komari Agent uninstall journal: %w", err)
	}
	if err := writeHostFileAtomic(journalPath, append(raw, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("agent: persist Komari Agent uninstall journal: %w", err)
	}
	return journal, nil
}

func removeJournaledKomariFile(path string, journal *komariRemovalJournal) error {
	snapshot, err := validateJournaledKomariFile(path, journal)
	if err != nil || !snapshot.Exists {
		return err
	}
	return removeHostFile(path)
}

func validateJournaledKomariFile(path string, journal *komariRemovalJournal) (hostFileSnapshot, error) {
	expected, ok := journal.Files[path]
	if !ok {
		return hostFileSnapshot{}, errors.New("agent: Komari Agent uninstall journal does not cover a managed path")
	}
	snapshot, err := captureHostFile(path)
	if err != nil || !snapshot.Exists {
		return snapshot, err
	}
	if !expected.Exists {
		return hostFileSnapshot{}, fmt.Errorf("agent: refusing to remove Komari Agent file created after uninstall began: %s", path)
	}
	digest := sha256.Sum256(snapshot.Data)
	if hex.EncodeToString(digest[:]) != expected.SHA256 {
		return hostFileSnapshot{}, fmt.Errorf("agent: refusing to remove changed Komari Agent file: %s", path)
	}
	return snapshot, nil
}

func declaredArtifact(manifest catalog.AppManifest, name string, target platform.Target) (catalog.Artifact, error) {
	for _, artifact := range manifest.Artifacts {
		if artifact.Name == name && artifact.OperatingSystem == target.OS && artifact.Architecture == target.Architecture {
			return artifact, nil
		}
	}
	return catalog.Artifact{}, fmt.Errorf("agent: manifest does not declare %s for %s/%s", name, target.OS, target.Architecture)
}

func normalizedKomariEndpoint(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid Komari endpoint")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func (manager SystemdHostApplicationManager) downloadArtifact(ctx context.Context, artifact catalog.Artifact) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("agent: create Komari Agent download: %w", err)
	}
	client := manager.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("agent: download Komari Agent: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent: download Komari Agent: unexpected HTTP status %d", response.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, maxArtifactBytes+1))
	if err != nil {
		return nil, fmt.Errorf("agent: read Komari Agent artifact: %w", err)
	}
	if len(content) == 0 || len(content) > maxArtifactBytes {
		return nil, errors.New("agent: Komari Agent artifact has an invalid size")
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != artifact.SHA256 {
		return nil, errors.New("agent: Komari Agent artifact integrity check failed")
	}
	return content, nil
}

func (manager SystemdHostApplicationManager) path(absolute string) string {
	if manager.RootDir == "" {
		return absolute
	}
	return filepath.Join(manager.RootDir, strings.TrimPrefix(absolute, "/"))
}

func (manager SystemdHostApplicationManager) run(ctx context.Context, name string, arguments ...string) error {
	if manager.RunCommand != nil {
		return manager.RunCommand(ctx, name, arguments...)
	}
	output, err := exec.CommandContext(ctx, name, arguments...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

type hostFileSnapshot struct {
	Path   string
	Data   []byte
	Mode   os.FileMode
	Exists bool
}

func captureHostFile(path string) (hostFileSnapshot, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return hostFileSnapshot{Path: path}, nil
	}
	if err != nil {
		return hostFileSnapshot{}, fmt.Errorf("agent: inspect managed host file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return hostFileSnapshot{}, fmt.Errorf("agent: refusing to replace non-regular managed host file %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return hostFileSnapshot{}, fmt.Errorf("agent: read managed host file %s: %w", path, err)
	}
	return hostFileSnapshot{Path: path, Data: data, Mode: info.Mode().Perm(), Exists: true}, nil
}

func restoreHostFiles(snapshots []hostFileSnapshot) error {
	var result error
	for _, snapshot := range snapshots {
		if snapshot.Exists {
			result = errors.Join(result, writeHostFileAtomic(snapshot.Path, snapshot.Data, snapshot.Mode))
		} else {
			result = errors.Join(result, removeHostFile(snapshot.Path))
		}
	}
	return result
}

func writeHostFileAtomic(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("agent: create managed host directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".vastora-*")
	if err != nil {
		return fmt.Errorf("agent: create managed host file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	err = temporary.Chmod(mode)
	if err == nil {
		_, err = temporary.Write(content)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("agent: write managed host file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("agent: replace managed host file: %w", err)
	}
	return nil
}

func removeHostFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("agent: remove managed host file %s: %w", path, err)
	}
	return nil
}
