package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/agent"
)

type systemHostDecommissioner struct {
	dataDir    string
	executable string
}

func (d systemHostDecommissioner) Prepare(ctx context.Context, deleteData bool) error {
	return agent.PurgeManagedRuntime(ctx, deleteData)
}

func (d systemHostDecommissioner) ScheduleFinalRemoval(ctx context.Context, deleteData bool) error {
	executable, err := filepath.Abs(d.executable)
	if err != nil {
		return fmt.Errorf("agent: resolve decommission executable: %w", err)
	}
	arguments := []string{"--unit=vastora-agent-decommission-" + strconv.FormatInt(time.Now().UnixNano(), 10), "--collect", "--on-active=15s", executable, "agent", "uninstall", "--purge", "--runtime-cleaned", "--data-dir", d.dataDir}
	if deleteData {
		arguments = append(arguments, "--delete-data")
	}
	output, err := exec.CommandContext(ctx, "systemd-run", arguments...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("agent: schedule final host cleanup: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func uninstallAgentHost(ctx context.Context, dataDir string, deleteData, runtimeCleaned, keepBinary bool) error {
	dataDir, err := safeAgentDataDir(dataDir)
	if err != nil {
		return err
	}
	return uninstallAgentHostWithEnvironment(ctx, deleteData, runtimeCleaned, keepBinary, agentUninstallEnvironment{
		dataDir:              dataDir,
		unitPath:             vastoraAgentUnitPath,
		binaryPaths:          []string{"/usr/local/bin/vastora", "/usr/local/bin/vastora.previous"},
		tailscalePaths:       []string{"/etc/apt/sources.list.d/tailscale.list", "/usr/share/keyrings/tailscale-archive-keyring.gpg", "/var/lib/tailscale"},
		tailscalePrivacyPath: "/etc/systemd/system/tailscaled.service.d/90-vastora-privacy.conf",
		purgeRuntime:         agent.PurgeManagedRuntime,
		run:                  runHostCommand,
	})
}

func safeAgentDataDir(value string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("resolve Agent state directory: %w", err)
	}
	absolute = filepath.Clean(absolute)
	for _, unsafe := range []string{"/", "/tmp", "/var", "/var/lib", "/var/lib/vastora"} {
		if absolute == unsafe {
			return "", fmt.Errorf("refusing unsafe Agent state directory: %s", absolute)
		}
	}
	if info, err := os.Lstat(absolute); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("refusing an Agent state directory reached through a symbolic link")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect Agent state directory: %w", err)
	}
	return absolute, nil
}

type agentUninstallEnvironment struct {
	dataDir              string
	unitPath             string
	binaryPaths          []string
	tailscalePaths       []string
	tailscalePrivacyPath string
	purgeRuntime         func(context.Context, bool) error
	run                  func(context.Context, string, ...string) ([]byte, error)
}

func runHostCommand(ctx context.Context, name string, arguments ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, arguments...).CombinedOutput()
}

func uninstallAgentHostWithEnvironment(ctx context.Context, deleteData, runtimeCleaned, keepBinary bool, environment agentUninstallEnvironment) error {
	state, err := agent.ReadHostInstallState(environment.dataDir)
	if err != nil {
		return err
	}
	statePath := filepath.Join(environment.dataDir, agent.HostInstallStateName)
	_, stateFileErr := os.Stat(statePath)
	stateRecorded := stateFileErr == nil
	if stateFileErr != nil && !errors.Is(stateFileErr, os.ErrNotExist) {
		return fmt.Errorf("inspect Agent host ownership state: %w", stateFileErr)
	}
	if !runtimeCleaned {
		if err := environment.purgeRuntime(ctx, deleteData); err != nil {
			return fmt.Errorf("remove managed Agent runtime: %w", err)
		}
	}
	if state.TailscaleEnrolled {
		output, logoutErr := environment.run(ctx, "tailscale", "logout")
		if logoutErr != nil && state.TailscaleOwnership != "managed" && !errors.Is(logoutErr, exec.ErrNotFound) {
			return fmt.Errorf("disconnect the external Tailscale installation from Vastora: %s: %w", strings.TrimSpace(string(output)), logoutErr)
		}
	}
	if state.TailscaleOwnership == "managed" {
		_, _ = environment.run(ctx, "systemctl", "disable", "--now", "tailscaled.service")
		if output, err := environment.run(ctx, "apt-get", "purge", "-y", "tailscale", "tailscale-archive-keyring"); err != nil {
			return fmt.Errorf("remove Vastora-managed Tailscale: %s: %w", strings.TrimSpace(string(output)), err)
		}
		for _, path := range environment.tailscalePaths {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("remove Vastora-managed Tailscale state %s: %w", path, err)
			}
		}
	}
	if environment.tailscalePrivacyPath != "" {
		privacyStateRemoved := false
		for _, path := range []string{environment.tailscalePrivacyPath, tailscalePrivacyAppliedPath(environment.tailscalePrivacyPath)} {
			if err := os.Remove(path); err == nil {
				privacyStateRemoved = true
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove Vastora Tailscale privacy state %s: %w", path, err)
			}
		}
		_ = os.Remove(filepath.Dir(environment.tailscalePrivacyPath))
		if privacyStateRemoved {
			if output, err := environment.run(ctx, "systemctl", "daemon-reload"); err != nil {
				return fmt.Errorf("reload systemd after removing the Tailscale privacy override: %s: %w", strings.TrimSpace(string(output)), err)
			}
		}
	}
	unitOwned, err := stopAndRemoveAgentUnit(ctx, environment)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(environment.dataDir); err != nil {
		return fmt.Errorf("remove Agent state: %w", err)
	}
	if !keepBinary && (unitOwned || stateRecorded) {
		for _, path := range environment.binaryPaths {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove Agent command %s: %w", path, err)
			}
		}
	}
	return nil
}

func stopAndRemoveAgentUnit(ctx context.Context, environment agentUninstallEnvironment) (bool, error) {
	raw, err := os.ReadFile(environment.unitPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect Agent service: %w", err)
	}
	if !strings.Contains(string(raw), "Description=Vastora Agent") || !strings.Contains(string(raw), " agent serve ") {
		return false, errors.New("refusing to remove an unrecognized vastora-agent.service")
	}
	if output, err := environment.run(ctx, "systemctl", "disable", "--now", "vastora-agent.service"); err != nil {
		return false, fmt.Errorf("stop Agent service: %s: %w", strings.TrimSpace(string(output)), err)
	}
	if err := os.Remove(environment.unitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("remove Agent service: %w", err)
	}
	if output, err := environment.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return false, fmt.Errorf("reload systemd after Agent removal: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return true, nil
}
