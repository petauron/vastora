package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/agent"
)

func TestAgentUninstallRetainsOwnershipUntilAllBinariesAreRemoved(t *testing.T) {
	for _, ownership := range []string{"unit", "state", "both"} {
		t.Run(ownership, func(t *testing.T) {
			environment := newAgentUninstallFixture(t)
			if ownership == "unit" {
				if err := os.Remove(filepath.Join(environment.dataDir, agent.HostInstallStateName)); err != nil {
					t.Fatal(err)
				}
			}
			if ownership == "state" {
				if err := os.Remove(environment.unitPath); err != nil {
					t.Fatal(err)
				}
			}
			// A non-empty directory deterministically interrupts os.Remove,
			// including when the test process has root permissions.
			blockedPath := environment.binaryPaths[1]
			if err := os.Remove(blockedPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(blockedPath, 0o700); err != nil {
				t.Fatal(err)
			}
			blocker := filepath.Join(blockedPath, "interruption")
			if err := os.WriteFile(blocker, []byte("keep until retry"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := uninstallAgentHostWithEnvironment(context.Background(), true, true, false, environment); err == nil || !strings.Contains(err.Error(), "remove Agent command") {
				t.Fatalf("binary deletion did not interrupt cleanup: %v", err)
			}
			assertUninstallPathsAbsent(t, environment.binaryPaths[0])
			if ownership != "state" {
				if _, err := os.Stat(environment.unitPath); err != nil {
					t.Fatalf("unit ownership evidence was discarded before cleanup finished: %v", err)
				}
			}
			if ownership != "unit" {
				if _, err := os.Stat(filepath.Join(environment.dataDir, agent.HostInstallStateName)); err != nil {
					t.Fatalf("host ownership evidence was discarded before cleanup finished: %v", err)
				}
			}
			if err := os.Remove(blocker); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := uninstallAgentHostWithEnvironment(context.Background(), true, true, false, environment); err != nil {
					t.Fatalf("resumed uninstall %d failed: %v", attempt, err)
				}
				assertUninstallPathsAbsent(t, environment.unitPath, environment.dataDir, environment.binaryPaths[0], blockedPath)
			}
		})
	}
}

func TestAgentUninstallRetriesReloadAfterFilesAreRemoved(t *testing.T) {
	environment := newAgentUninstallFixture(t)
	reloads := 0
	environment.run = func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		if name == "systemctl" && len(arguments) == 1 && arguments[0] == "daemon-reload" {
			reloads++
			if reloads == 1 {
				return nil, errors.New("interrupted systemd reload")
			}
		}
		return nil, nil
	}
	if err := uninstallAgentHostWithEnvironment(context.Background(), true, true, false, environment); err == nil || !strings.Contains(err.Error(), "reload systemd") {
		t.Fatalf("reload failure was not reported: %v", err)
	}
	assertUninstallPathsAbsent(t, environment.unitPath, environment.dataDir, environment.binaryPaths[0], environment.binaryPaths[1])
	if err := uninstallAgentHostWithEnvironment(context.Background(), true, true, false, environment); err != nil {
		t.Fatalf("reload retry failed: %v", err)
	}
	if reloads != 2 {
		t.Fatalf("reload calls = %d, want 2", reloads)
	}
}

func TestAgentUninstallStopsBeforeRemovingRuntime(t *testing.T) {
	environment := newAgentUninstallFixture(t)
	runtimeRemoved := false
	environment.purgeRuntime = func(context.Context, bool) error {
		runtimeRemoved = true
		return nil
	}
	environment.run = func(_ context.Context, _ string, arguments ...string) ([]byte, error) {
		if len(arguments) != 0 && arguments[0] == "disable" {
			return nil, errors.New("Agent still running")
		}
		return nil, nil
	}
	if err := uninstallAgentHostWithEnvironment(context.Background(), true, false, false, environment); err == nil || !strings.Contains(err.Error(), "stop Agent service") {
		t.Fatalf("failed stop did not block cleanup: %v", err)
	}
	if runtimeRemoved {
		t.Fatal("runtime was removed while the Agent could restore it")
	}
	for _, path := range append([]string{environment.unitPath, environment.dataDir}, environment.binaryPaths...) {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("failed stop removed %s: %v", path, err)
		}
	}
}

func TestAgentUninstallPreservesBinaryWhenRequested(t *testing.T) {
	environment := newAgentUninstallFixture(t)
	if err := uninstallAgentHostWithEnvironment(context.Background(), false, true, true, environment); err != nil {
		t.Fatal(err)
	}
	assertUninstallPathsAbsent(t, environment.unitPath, environment.dataDir)
	for _, path := range environment.binaryPaths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("requested retained binary was removed: %s: %v", path, err)
		}
	}
}

func TestAgentUninstallRejectsInvalidOwnershipBeforeHostChanges(t *testing.T) {
	for _, kind := range []string{"symlink", "permissions", "ambiguous-record"} {
		t.Run(kind, func(t *testing.T) {
			environment := newAgentUninstallFixture(t)
			path := filepath.Join(environment.dataDir, agent.HostInstallStateName)
			switch kind {
			case "symlink":
				target := filepath.Join(t.TempDir(), "state")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(path, 0o666); err != nil {
					t.Fatal(err)
				}
			case "ambiguous-record":
				if err := os.WriteFile(path, []byte("HOST_STATE_VERSION=1\nTAILSCALE_OWNERSHIP=external\nTAILSCALE_OWNERSHIP=managed\nTAILSCALE_ENROLLED=1\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			environment.run = func(context.Context, string, ...string) ([]byte, error) {
				t.Fatal("invalid provenance triggered a host command")
				return nil, nil
			}
			environment.purgeRuntime = func(context.Context, bool) error {
				t.Fatal("invalid provenance triggered runtime removal")
				return nil
			}
			if err := uninstallAgentHostWithEnvironment(context.Background(), true, false, false, environment); err == nil {
				t.Fatal("uninstall accepted untrustworthy ownership evidence")
			}
			for _, path := range append([]string{path, environment.unitPath}, environment.binaryPaths...) {
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("invalid provenance caused file removal: %s (%v)", path, err)
				}
			}
		})
	}
}

func newAgentUninstallFixture(t *testing.T) agentUninstallEnvironment {
	t.Helper()
	root := t.TempDir()
	environment := agentUninstallEnvironment{
		dataDir:      filepath.Join(root, "agent"),
		unitPath:     filepath.Join(root, "vastora-agent.service"),
		binaryPaths:  []string{filepath.Join(root, "vastora"), filepath.Join(root, "vastora.previous")},
		purgeRuntime: func(context.Context, bool) error { return nil },
		run:          func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
	}
	if err := os.Mkdir(environment.dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(environment.dataDir, agent.HostInstallStateName): "HOST_STATE_VERSION=1\nTAILSCALE_OWNERSHIP=external\nTAILSCALE_ENROLLED=0\n",
		environment.unitPath: "Description=Vastora Agent\nExecStart=/usr/local/bin/vastora agent serve --data-dir /var/lib/vastora/agent\n",
	}
	for _, path := range environment.binaryPaths {
		files[path] = "synthetic managed binary"
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return environment
}

func assertUninstallPathsAbsent(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cleanup left %s: %v", path, err)
		}
	}
}
