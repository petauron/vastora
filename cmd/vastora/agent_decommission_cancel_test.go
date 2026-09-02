package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/agent"
)

func TestLocalUninstallCancelsPendingHostCleanupWithoutCenter(t *testing.T) {
	for _, phase := range []string{"pending", "finalizing"} {
		t.Run(phase, func(t *testing.T) {
			environment, dataDir := newHostCancellationFixture(t)
			if phase == "pending" {
				if err := os.Remove(environment.generatorPath); err != nil {
					t.Fatal(err)
				}
			} else if phase == "finalizing" {
				if err := os.Remove(filepath.Join(environment.directory, "operation.json")); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := cancelHostHelper(context.Background(), dataDir, environment); err != nil {
					t.Fatal(err)
				}
				assertUninstallPathsAbsent(t, environment.directory, environment.unitPath, environment.enabledLink, environment.generatorPath)
			}
		})
	}
}

func TestLocalCleanupCancellationRemainsEffectiveAfterStopFailure(t *testing.T) {
	environment, dataDir := newHostCancellationFixture(t)
	environment.run = func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		if name == "systemctl" && arguments[0] == "stop" {
			return nil, errors.New("stop interrupted")
		}
		if name == "systemctl" && arguments[0] == "show" {
			return []byte("LoadState=loaded\nActiveState=active\n"), nil
		}
		return nil, nil
	}
	if err := cancelHostHelper(context.Background(), dataDir, environment); err == nil {
		t.Fatal("stop failure was ignored")
	}
	operationPath := filepath.Join(environment.directory, "operation.json")
	if err := runHostDecommission(context.Background(), operationPath, agent.Client{}, func(context.Context, hostDecommissionOperation) error {
		t.Fatal("cancelled helper continued destructive cleanup")
		return nil
	}); !errors.Is(err, errHostDecommissionCancelled) {
		t.Fatalf("restart did not honor local cancellation before networking: %v", err)
	}
	if runtime.GOOS != "windows" {
		output := t.TempDir()
		if result, err := exec.Command(environment.generatorPath, output, output, output).CombinedOutput(); err != nil {
			t.Fatalf("cancelled generator failed: %s (%v)", result, err)
		}
		assertUninstallPathsAbsent(t, filepath.Join(output, hostDecommissionUnitName))
	}
	environment.run = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	if err := cancelHostHelper(context.Background(), dataDir, environment); err != nil {
		t.Fatalf("local cancellation did not resume: %v", err)
	}
	assertUninstallPathsAbsent(t, environment.directory, environment.unitPath, environment.enabledLink, environment.generatorPath)
}

func TestLocalCleanupCancellationRetriesAfterReloadAndDeletionFailure(t *testing.T) {
	for _, phase := range []string{"reload", "binary"} {
		t.Run(phase, func(t *testing.T) {
			environment, dataDir := newHostCancellationFixture(t)
			binaryPath := filepath.Join(environment.directory, "vastora")
			if phase == "binary" {
				if err := os.Remove(binaryPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(binaryPath, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(binaryPath, "blocker"), []byte("interruption"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				environment.run = func(_ context.Context, name string, arguments ...string) ([]byte, error) {
					if name == "systemctl" && arguments[0] == "daemon-reload" {
						return nil, errors.New("reload interrupted")
					}
					return nil, nil
				}
			}
			if err := cancelHostHelper(context.Background(), dataDir, environment); err == nil {
				t.Fatal("cleanup interruption was ignored")
			}
			if cancelled, err := protectedCleanupMarkerExists(filepath.Join(environment.directory, "cancelled"), "cancelled\n"); err != nil || !cancelled {
				t.Fatalf("lost local cancellation after partial cleanup: %v", err)
			}
			if phase == "binary" {
				if err := os.Remove(filepath.Join(binaryPath, "blocker")); err != nil {
					t.Fatal(err)
				}
			}
			environment.run = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
			if err := cancelHostHelper(context.Background(), dataDir, environment); err != nil {
				t.Fatalf("partial cancellation did not resume: %v", err)
			}
			assertUninstallPathsAbsent(t, environment.directory, environment.unitPath, environment.enabledLink, environment.generatorPath)
		})
	}
}

func TestLocalCleanupCancellationRejectsUnrelatedState(t *testing.T) {
	for _, changed := range []string{"unit", "generator", "link", "agent"} {
		t.Run(changed, func(t *testing.T) {
			environment, dataDir := newHostCancellationFixture(t)
			switch changed {
			case "unit", "generator":
				path := environment.unitPath
				if changed == "generator" {
					path = environment.generatorPath
				}
				if err := os.WriteFile(path, []byte("operator replacement"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "link":
				if err := os.Remove(environment.enabledLink); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "other.service"), environment.enabledLink); err != nil {
					t.Fatal(err)
				}
			case "agent":
				dataDir = filepath.Join(t.TempDir(), "other-agent")
			}
			environment.run = func(context.Context, string, ...string) ([]byte, error) {
				t.Fatal("unrelated state triggered a host command")
				return nil, nil
			}
			if err := cancelHostHelper(context.Background(), dataDir, environment); err == nil {
				t.Fatal("unrelated cleanup state was accepted")
			}
			for _, path := range []string{environment.directory, environment.unitPath, environment.enabledLink, environment.generatorPath} {
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("unrelated state was removed: %s (%v)", path, err)
				}
			}
			assertUninstallPathsAbsent(t, filepath.Join(environment.directory, "cancelled"))
		})
	}
}

func TestLocalCleanupCancellationAcceptsMissingInactiveService(t *testing.T) {
	environment, dataDir := newHostCancellationFixture(t)
	environment.run = func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		if name == "systemctl" && arguments[0] == "stop" {
			return nil, errors.New("unit not loaded")
		}
		if name == "systemctl" && arguments[0] == "show" {
			return []byte("LoadState=not-found\nActiveState=inactive\n"), nil
		}
		return nil, nil
	}
	if err := cancelHostHelper(context.Background(), dataDir, environment); err != nil {
		t.Fatal(err)
	}
	assertUninstallPathsAbsent(t, environment.directory, environment.unitPath, environment.enabledLink, environment.generatorPath)
}

func newHostCancellationFixture(t *testing.T) (hostHelperCancellationEnvironment, string) {
	t.Helper()
	directory, unitPath, enabledLink, generatorPath := newHostFinalizerFixture(t)
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(t.TempDir(), "agent")
	operation := hostDecommissionOperation{Version: 2, TaskID: "agent-decommission-fixture", AgentID: "fixture", Attempt: 1, DataDir: dataDir, CenterURL: "http://127.0.0.1:1", Credential: "synthetic-offline-credential", CallbackURL: "http://127.0.0.1:1/api/v1/agent-decommission-results/agent-decommission-fixture", CallbackToken: "synthetic-callback-token"}
	raw, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRootFileAtomic(filepath.Join(directory, "operation.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return hostHelperCancellationEnvironment{
		directory: directory, unitName: hostDecommissionUnitName, unitPath: unitPath, unitContents: hostDecommissionServiceUnit(), enabledLink: enabledLink, generatorPath: generatorPath,
		generatorContents: hostDecommissionGeneratorScript(directory, hostDecommissionFinalizerUnit(directory, unitPath, enabledLink, generatorPath)), operationDataDir: hostDecommissionDataDir,
		run: func(_ context.Context, name string, arguments ...string) ([]byte, error) {
			if name != "systemctl" && name != "sync" {
				t.Fatalf("unexpected cancellation command: %s %s", name, strings.Join(arguments, " "))
			}
			return nil, nil
		},
	}, dataDir
}
