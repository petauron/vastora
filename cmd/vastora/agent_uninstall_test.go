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

func TestAgentUninstallPreservesApplicationDataInPlaceByDefault(t *testing.T) {
	for _, deleteData := range []bool{false, true} {
		t.Run(map[bool]string{false: "retain", true: "delete"}[deleteData], func(t *testing.T) {
			environment := newAgentUninstallFixture(t)
			paths := []string{"packages/vastora-pkg-fixture/resources.json", "packages/vastora-pkg-fixture/backups/snapshot/data.tar", "meridian/state.db", "xray-worker/config.json", "agent.db", "agent.key", "unknown/data"}
			for _, relative := range paths {
				path := filepath.Join(environment.dataDir, relative)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(relative), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := uninstallAgentHostWithEnvironment(context.Background(), deleteData, true, false, environment); err != nil {
					t.Fatal(err)
				}
				assertUninstallPathsAbsent(t, environment.unitPath, environment.binaryPaths[0], environment.binaryPaths[1])
				if deleteData {
					assertUninstallPathsAbsent(t, environment.dataDir)
					continue
				}
				for _, relative := range paths {
					raw, err := os.ReadFile(filepath.Join(environment.dataDir, relative))
					if err != nil || string(raw) != relative {
						t.Fatalf("retained data changed: %s: %v", relative, err)
					}
				}
			}
		})
	}
}

func TestAgentStateCleanupRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(root, "agent")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for _, deleteData := range []bool{false, true} {
		if err := removeAgentState(link, deleteData); err == nil {
			t.Fatal("symlink state directory accepted")
		}
	}
}

func TestAgentUninstallStopsOnTailscaleCommandFailure(t *testing.T) {
	for _, failure := range []string{"logout", "disable"} {
		t.Run(failure, func(t *testing.T) {
			environment := newAgentUninstallFixture(t)
			statePath := filepath.Join(environment.dataDir, agent.HostInstallStateName)
			if err := os.WriteFile(statePath, []byte("HOST_STATE_VERSION=1\nTAILSCALE_OWNERSHIP=managed\nTAILSCALE_ENROLLED=1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			failed, afterFailure := false, 0
			environment.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if failed {
					afterFailure++
				}
				if (failure == "logout" && name == "tailscale") || (failure == "disable" && name == "systemctl" && strings.Join(args, " ") == "disable --now tailscaled.service") {
					failed = true
					return nil, errors.New("injected command failure")
				}
				return nil, nil
			}
			if err := uninstallAgentHostWithEnvironment(context.Background(), true, true, false, environment); err == nil || !failed || afterFailure != 0 {
				t.Fatalf("failure did not stop cleanup: failed=%v following=%d err=%v", failed, afterFailure, err)
			}
			for _, path := range append([]string{statePath, environment.unitPath}, environment.binaryPaths...) {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("failure discarded remaining file %s: %v", path, err)
				}
			}
		})
	}
}

func TestHostDecommissionHelperDoesNotAutostart(t *testing.T) {
	unit := hostDecommissionServiceUnit()
	if !strings.Contains(unit, "Restart=no\n") || strings.Contains(unit, "WantedBy=") || strings.Contains(unit, "RestartSec=") {
		t.Fatal("helper can restart or run at boot without authorization")
	}
}

func TestAgentUninstallCancellationStopsFollowingMutations(t *testing.T) {
	for _, phase := range []string{"before-start", "stop-agent", "runtime", "logout", "stop-tailscale", "purge-tailscale"} {
		t.Run(phase, func(t *testing.T) {
			environment := newAgentUninstallFixture(t)
			statePath := filepath.Join(environment.dataDir, agent.HostInstallStateName)
			if err := os.WriteFile(statePath, []byte("HOST_STATE_VERSION=1\nTAILSCALE_OWNERSHIP=managed\nTAILSCALE_ENROLLED=1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			afterCancel := 0
			environment.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if ctx.Err() != nil {
					afterCancel++
				}
				command := name + " " + strings.Join(args, " ")
				if (phase == "stop-agent" && command == "systemctl disable --now vastora-agent.service") || (phase == "logout" && name == "tailscale") || (phase == "stop-tailscale" && command == "systemctl disable --now tailscaled.service") || (phase == "purge-tailscale" && name == "apt-get") {
					cancel()
				}
				return nil, nil
			}
			environment.purgeRuntime = func(ctx context.Context, _ bool) error {
				if ctx.Err() != nil {
					afterCancel++
				}
				if phase == "runtime" {
					cancel()
				}
				return nil
			}
			if phase == "before-start" {
				cancel()
			}
			err := uninstallAgentHostWithEnvironment(ctx, true, false, false, environment)
			if !errors.Is(err, context.Canceled) || afterCancel != 0 {
				t.Fatalf("cancellation ignored: err=%v following=%d", err, afterCancel)
			}
			for _, path := range append([]string{statePath, environment.unitPath}, environment.binaryPaths...) {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("cancelled cleanup removed %s: %v", path, err)
				}
			}
		})
	}
}

func TestAgentUninstallStopsBeforeUnauthorizedStage(t *testing.T) {
	for _, denied := range []string{"command", "runtime", "files"} {
		t.Run(denied, func(t *testing.T) {
			environment := newAgentUninstallFixture(t)
			refused, following := false, 0
			denial := errors.New("Center rejected cleanup stage")
			environment.authorize = func(_ context.Context, phase string) error {
				if refused {
					following++
				}
				if phase == denied {
					refused = true
					return denial
				}
				return nil
			}
			environment.run = func(context.Context, string, ...string) ([]byte, error) {
				if refused {
					following++
				}
				return nil, nil
			}
			environment.purgeRuntime = func(context.Context, bool) error {
				if refused {
					following++
				}
				return nil
			}
			if err := uninstallAgentHostWithEnvironment(context.Background(), true, false, false, environment); !errors.Is(err, denial) || !refused || following != 0 {
				t.Fatalf("unauthorized continuation: refused=%v following=%d err=%v", refused, following, err)
			}
			for _, path := range append([]string{environment.dataDir, environment.unitPath}, environment.binaryPaths...) {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("unauthorized removal: %s %v", path, err)
				}
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
