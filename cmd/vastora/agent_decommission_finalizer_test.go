package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestHostDecommissionFinalizerCommandsResumeAfterInterruption(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host finalization uses Linux host utilities")
	}
	for completed := 0; completed <= len(hostDecommissionFinalizerCommands("state", "unit", "enabled", "generator")); completed++ {
		t.Run(strconv.Itoa(completed), func(t *testing.T) {
			directory, unitPath, enabledLink, generatorPath := newHostFinalizerFixture(t)
			commands := hostDecommissionFinalizerCommands(directory, unitPath, enabledLink, generatorPath)
			for _, command := range commands[:completed] {
				if err := runHostFinalizerFixtureCommand(command); err != nil {
					t.Fatal(err)
				}
			}
			// Model a fresh boot after this interruption: while anything
			// persistent remains, the generator must recreate the runnable
			// finalizer without the binary, operation, unit, or enable link.
			if _, err := os.Stat(generatorPath); err == nil {
				verifyHostFinalizerGenerator(t, generatorPath, hostDecommissionFinalizerUnit(directory, unitPath, enabledLink, generatorPath))
			} else {
				assertUninstallPathsAbsent(t, directory, unitPath, enabledLink)
			}
			for range 2 {
				for _, command := range commands {
					if err := runHostFinalizerFixtureCommand(command); err != nil {
						t.Fatal(err)
					}
				}
				assertUninstallPathsAbsent(t, directory, unitPath, enabledLink, generatorPath)
			}
		})
	}
}

func TestHostDecommissionFinalizerRetainsServiceWhenStateRemovalFails(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host finalization uses Linux host utilities")
	}
	directory, unitPath, enabledLink, generatorPath := newHostFinalizerFixture(t)
	unrelated := filepath.Join(directory, "operator-file")
	if err := os.WriteFile(unrelated, []byte("must survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	commands := hostDecommissionFinalizerCommands(directory, unitPath, enabledLink, generatorPath)
	failed := false
	for _, command := range commands {
		if err := runHostFinalizerFixtureCommand(command); err != nil {
			failed = true
			break
		}
	}
	if !failed {
		t.Fatal("finalizer accepted unexpected directory content")
	}
	for _, path := range []string{unitPath, enabledLink, generatorPath, unrelated} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("finalizer removed recovery state or an unrelated file: %s (%v)", path, err)
		}
	}
	if err := os.Remove(unrelated); err != nil {
		t.Fatal(err)
	}
	for _, command := range commands {
		if err := runHostFinalizerFixtureCommand(command); err != nil {
			t.Fatal(err)
		}
	}
	assertUninstallPathsAbsent(t, directory, unitPath, enabledLink, generatorPath)
}

func TestHostDecommissionFinalizerActivationRetainsIndependentUnit(t *testing.T) {
	for _, failure := range []string{"daemon-reload", "restart"} {
		t.Run(failure, func(t *testing.T) {
			directory, unitPath, enabledLink, generatorPath := newHostFinalizerFixture(t)
			finalizer := hostDecommissionFinalizerUnit(directory, unitPath, enabledLink, generatorPath)
			generator := hostDecommissionGeneratorScript(directory, finalizer)
			if err := os.Remove(generatorPath); err != nil {
				t.Fatal(err)
			}
			injected := false
			run := func(_ context.Context, _ string, arguments ...string) ([]byte, error) {
				if !injected && arguments[0] == failure {
					injected = true
					return nil, errors.New("interrupted activation")
				}
				return nil, nil
			}
			if err := activateHostDecommissionFinalizer(context.Background(), unitPath, generatorPath, generator, run); err == nil {
				t.Fatal("activation failure was not reported")
			}
			if got, err := os.ReadFile(generatorPath); err != nil || string(got) != generator {
				t.Fatalf("independent finalization intent was not retained: %v", err)
			}
			if err := activateHostDecommissionFinalizer(context.Background(), unitPath, generatorPath, generator, run); err != nil {
				t.Fatalf("finalizer activation did not resume: %v", err)
			}
			if strings.Contains(finalizer, "ExecStopPost=") || strings.Contains(finalizer, " agent ") || !strings.Contains(finalizer, "Type=oneshot\n") || !strings.Contains(finalizer, "Restart=on-failure\n") {
				t.Fatal("finalizer still depends on the Agent binary or has no ordered retry")
			}
			if strings.Count(finalizer, "ExecStart=") != len(hostDecommissionFinalizerCommands(directory, unitPath, enabledLink, generatorPath)) {
				t.Fatal("finalizer omitted a cleanup command")
			}
		})
	}
}

func TestHostDecommissionSchedulingDoesNotReplacePendingCleanup(t *testing.T) {
	directory, unitPath, enabledLink, generatorPath := newHostFinalizerFixture(t)
	operationPath, operation := writeHostDecommissionFixture(t, filepath.Join(t.TempDir(), "agent"), "http://127.0.0.1:1")
	if err := checkHostDecommissionOwnership(unitPath, operationPath, generatorPath, operation); err == nil {
		t.Fatal("new scheduling overwrote an independent finalizer")
	}
	if err := os.Remove(generatorPath); err != nil {
		t.Fatal(err)
	}
	if err := checkHostDecommissionOwnership(unitPath, operationPath, generatorPath, operation); err != nil {
		t.Fatalf("same pending operation was rejected: %v", err)
	}
	operation.Attempt++
	if err := checkHostDecommissionOwnership(unitPath, operationPath, generatorPath, operation); err == nil {
		t.Fatal("new attempt replaced a pending operation")
	}
	if err := writeRootFileAtomic(unitPath, []byte("unrelated service"), 0o644); err != nil {
		t.Fatal(err)
	}
	generator := hostDecommissionGeneratorScript(directory, hostDecommissionFinalizerUnit(directory, unitPath, enabledLink, generatorPath))
	if err := activateHostDecommissionFinalizer(context.Background(), unitPath, generatorPath, generator, func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("unrelated service triggered systemctl")
		return nil, nil
	}); err == nil {
		t.Fatal("finalizer replaced an unrelated service")
	}
}

func TestHostDecommissionFinalizerUnitIsAcceptedBySystemd(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd is a Linux host dependency")
	}
	verifier, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze is not installed")
	}
	directory, unitPath, enabledLink, generatorPath := newHostFinalizerFixture(t)
	finalizer := hostDecommissionFinalizerUnit(directory, unitPath, enabledLink, generatorPath)
	if err := writeRootFileAtomic(unitPath, []byte(finalizer), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(verifier, "verify", unitPath).CombinedOutput(); err != nil {
		t.Fatalf("systemd rejected finalizer unit: %s (%v)", output, err)
	}
}

func newHostFinalizerFixture(t *testing.T) (directory, unitPath, enabledLink, generatorPath string) {
	t.Helper()
	root := t.TempDir()
	directory = filepath.Join(root, "cleanup")
	unitPath = filepath.Join(root, "systemd", hostDecommissionUnitName)
	enabledLink = filepath.Join(root, "enabled", hostDecommissionUnitName)
	generatorPath = filepath.Join(root, "generators", "vastora-agent-decommission")
	for _, name := range []string{"result.json", "operation.json", "completed", "vastora"} {
		if err := writeRootFileAtomic(filepath.Join(directory, name), []byte("synthetic fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeRootFileAtomic(unitPath, []byte(hostDecommissionServiceUnit()), 0o644); err != nil {
		t.Fatal(err)
	}
	generator := hostDecommissionGeneratorScript(directory, hostDecommissionFinalizerUnit(directory, unitPath, enabledLink, generatorPath))
	if err := writeRootFileAtomic(generatorPath, []byte(generator), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(enabledLink), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(unitPath, enabledLink); err != nil {
		t.Fatal(err)
	}
	return directory, unitPath, enabledLink, generatorPath
}

func runHostFinalizerFixtureCommand(command []string) error {
	if command[0] == "/usr/bin/systemctl" || command[0] == "/usr/bin/sync" {
		return nil // Never reload the test host or flush its entire filesystem.
	}
	return exec.Command(command[0], command[1:]...).Run()
}

func verifyHostFinalizerGenerator(t *testing.T, generatorPath, finalizer string) {
	t.Helper()
	output := t.TempDir()
	normal, early, late := filepath.Join(output, "normal"), filepath.Join(output, "early"), filepath.Join(output, "late")
	if output, err := exec.Command(generatorPath, normal, early, late).CombinedOutput(); err != nil {
		t.Fatalf("restart generator failed: %s (%v)", output, err)
	}
	generatedPath := filepath.Join(early, hostDecommissionUnitName)
	if got, err := os.ReadFile(generatedPath); err != nil || string(got) != finalizer {
		t.Fatalf("restart did not recover the same finalizer: %v", err)
	}
	if target, err := filepath.EvalSymlinks(filepath.Join(early, "multi-user.target.wants", hostDecommissionUnitName)); err != nil || target != generatedPath {
		t.Fatalf("restart did not enable the finalizer: %v", err)
	}
	assertUninstallPathsAbsent(t, normal, late)
}
