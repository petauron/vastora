package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type hostHelperCancellationEnvironment struct {
	directory         string
	unitName          string
	unitPath          string
	unitContents      string
	enabledLink       string
	generatorPath     string
	generatorContents string
	operationDataDir  func(string) (string, error)
	run               func(context.Context, string, ...string) ([]byte, error)
}

// An explicit local uninstall takes ownership from the autonomous helper.
// Never call this from that helper: its normal completion must still be
// acknowledged by Center before it discards its operation and credentials.
func uninstallAgentHostLocally(ctx context.Context, dataDir string, deleteData, runtimeCleaned, keepBinary bool) error {
	dataDir, err := safeAgentDataDir(dataDir)
	if err != nil {
		return err
	}
	if _, err := stopAgentUnit(ctx, agentUninstallEnvironment{unitPath: vastoraAgentUnitPath, run: runHostCommand}); err != nil {
		return err
	}
	if err := cancelHostHelper(ctx, dataDir, hostHelperCancellationEnvironment{
		directory: hostDecommissionDir, unitName: hostDecommissionUnitName, unitPath: hostDecommissionUnit, unitContents: hostDecommissionServiceUnit(), enabledLink: hostDecommissionEnabledLink,
		generatorPath:     hostDecommissionGenerator,
		generatorContents: hostDecommissionGeneratorScript(hostDecommissionDir, hostDecommissionFinalizerUnit(hostDecommissionDir, hostDecommissionUnit, hostDecommissionEnabledLink, hostDecommissionGenerator)),
		operationDataDir:  hostDecommissionDataDir, run: runHostCommand,
	}); err != nil {
		return err
	}
	return uninstallAgentHost(ctx, dataDir, deleteData, runtimeCleaned, keepBinary)
}

func cancelHostHelper(ctx context.Context, dataDir string, environment hostHelperCancellationEnvironment) error {
	unitExists, err := ownedHostCleanupFile(environment.unitPath, environment.unitContents)
	if err != nil {
		return err
	}
	generatorExists := false
	if environment.generatorPath != "" {
		generatorExists, err = ownedHostCleanupFile(environment.generatorPath, environment.generatorContents)
		if err != nil {
			return err
		}
	}
	directoryExists := false
	if info, err := os.Lstat(environment.directory); err == nil {
		if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("agent: host cleanup state is not a protected directory")
		}
		directoryExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	operationPath := filepath.Join(environment.directory, "operation.json")
	operationExists := false
	if _, err := os.Lstat(operationPath); err == nil {
		operationDataDir, err := environment.operationDataDir(operationPath)
		if err != nil {
			return err
		}
		if filepath.Clean(operationDataDir) != filepath.Clean(dataDir) {
			return errors.New("agent: pending host cleanup belongs to a different Agent state directory")
		}
		operationExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	linkExists := false
	if target, err := os.Readlink(environment.enabledLink); err == nil {
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(environment.enabledLink), target)
		}
		if filepath.Clean(target) != filepath.Clean(environment.unitPath) {
			return errors.New("agent: host cleanup enable link belongs to another service")
		}
		linkExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("agent: inspect host cleanup enable link: %w", err)
	}
	cancelledPath := filepath.Join(environment.directory, "cancelled")
	cancelled, err := protectedCleanupMarkerExists(cancelledPath, "cancelled\n")
	if err != nil {
		return err
	}
	if !unitExists && !generatorExists && !operationExists && !linkExists && !cancelled {
		if directoryExists {
			// Only an empty directory can remain after the final unlink was
			// interrupted. Never recurse into state without ownership proof.
			if err := os.Remove(environment.directory); err != nil {
				return fmt.Errorf("agent: refusing to remove unproven host cleanup state: %w", err)
			}
		}
		return nil
	}
	if err := os.MkdirAll(environment.directory, 0o700); err != nil {
		return err
	}
	if err := writeRootFileAtomic(cancelledPath, []byte("cancelled\n"), 0o600); err != nil {
		return err
	}
	if output, err := environment.run(ctx, "sync", "-f", environment.directory); err != nil {
		return fmt.Errorf("agent: persist local host cleanup cancellation: %s: %w", strings.TrimSpace(string(output)), err)
	}
	if output, err := environment.run(ctx, "systemctl", "stop", environment.unitName); err != nil {
		status, statusErr := environment.run(ctx, "systemctl", "show", "--property=LoadState", "--property=ActiveState", environment.unitName)
		lines := "\n" + strings.TrimSpace(string(status)) + "\n"
		if statusErr != nil || !strings.Contains(lines, "\nLoadState=not-found\n") || !strings.Contains(lines, "\nActiveState=inactive\n") {
			return fmt.Errorf("agent: stop pending host cleanup before local uninstall: %s: %w", strings.TrimSpace(string(output)), err)
		}
	}
	// Keep the cancellation marker until all restart mechanisms have been
	// removed and synced. A crash must not reactivate a cancelled operation.
	for _, path := range []string{environment.generatorPath, environment.enabledLink, environment.unitPath} {
		if path == "" {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("agent: remove cancelled host cleanup service: %w", err)
		}
		if _, err := os.Stat(filepath.Dir(path)); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if output, err := environment.run(ctx, "sync", "-f", filepath.Dir(path)); err != nil {
			return fmt.Errorf("agent: sync cancelled host cleanup service removal: %s: %w", strings.TrimSpace(string(output)), err)
		}
	}
	if output, err := environment.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("agent: reload systemd after local cleanup cancellation: %s: %w", strings.TrimSpace(string(output)), err)
	}
	for _, name := range []string{"result.json", "operation.json", "completed", "vastora"} {
		if err := os.Remove(filepath.Join(environment.directory, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("agent: remove cancelled host cleanup state: %w", err)
		}
	}
	entries, err := os.ReadDir(environment.directory)
	if err != nil {
		return err
	}
	if len(entries) != 1 || entries[0].Name() != "cancelled" {
		return errors.New("agent: unexpected files remain in cancelled host cleanup state")
	}
	if output, err := environment.run(ctx, "sync", "-f", environment.directory); err != nil {
		return fmt.Errorf("agent: sync cancelled host cleanup state removal: %s: %w", strings.TrimSpace(string(output)), err)
	}
	if err := os.Remove(cancelledPath); err != nil {
		return err
	}
	if err := os.Remove(environment.directory); err != nil {
		return err
	}
	if output, err := environment.run(ctx, "sync", "-f", filepath.Dir(environment.directory)); err != nil {
		return fmt.Errorf("agent: sync completed local cleanup cancellation: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func hostDecommissionDataDir(path string) (string, error) {
	operation, err := readHostDecommissionOperation(path)
	return operation.DataDir, err
}

func ownedHostCleanupFile(path, expected string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return false, errors.New("agent: refusing to cancel an unprotected host cleanup file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if string(raw) != expected {
		return false, errors.New("agent: refusing to cancel an unrelated host cleanup file")
	}
	return true, nil
}
