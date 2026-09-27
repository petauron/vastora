package agent

import (
	"context"
	"errors"
	"fmt"
	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/platform"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Historical paths are retained only for verified, one-shot ownership adoption.
const (
	komariBinaryPath = "/opt/komari/agent"
	komariConfigPath = "/etc/komari-agent/config.json"
	komariUnitPath   = "/etc/systemd/system/komari-agent.service"
	komariUnitMarker = "# Managed by Vastora\n"
)

type SystemdHostApplicationManager struct {
	RootDir     string
	HTTPClient  *http.Client
	RunCommand  func(context.Context, string, ...string) error
	ReadCommand func(context.Context, string, ...string) ([]byte, error)
	HostTarget  platform.Target
}

func declaredArtifact(manifest catalog.AppManifest, name string, target platform.Target) (catalog.Artifact, error) {
	for _, artifact := range manifest.Artifacts {
		if artifact.Name == name && artifact.OperatingSystem == target.OS && artifact.Architecture == target.Architecture {
			return artifact, nil
		}
	}
	return catalog.Artifact{}, fmt.Errorf("agent: manifest does not declare %s for %s/%s", name, target.OS, target.Architecture)
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
