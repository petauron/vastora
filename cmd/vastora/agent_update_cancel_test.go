package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUninstallRemovesPendingUpdaterBeforeAgentFiles(t *testing.T) {
	agentEnvironment := newAgentUninstallFixture(t)
	updateEnvironment := newHostUpdateCancellationFixture(t, agentEnvironment.dataDir)
	stopped := false
	updateEnvironment.run = func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		if name == "systemctl" && arguments[0] == "stop" {
			if arguments[1] != hostUpdateUnitName {
				t.Fatal("uninstall stopped the wrong helper")
			}
			cancelled, err := hostUpdateCancelled(filepath.Join(updateEnvironment.directory, "operation.json"))
			if err != nil || !cancelled {
				t.Fatalf("updater stop was not preceded by durable cancellation: %v", err)
			}
			stopped = true
		}
		if name == "systemctl" && arguments[0] == "start" {
			t.Fatal("uninstall restarted the Agent")
		}
		return nil, nil
	}
	agentEnvironment.pendingUpdate = &updateEnvironment
	agentEnvironment.purgeRuntime = func(context.Context, bool) error {
		if !stopped {
			t.Fatal("runtime removal started before updater cancellation")
		}
		assertUninstallPathsAbsent(t, updateEnvironment.directory, updateEnvironment.unitPath, updateEnvironment.enabledLink)
		return nil
	}
	if err := uninstallAgentHostWithEnvironment(context.Background(), true, false, false, agentEnvironment); err != nil {
		t.Fatal(err)
	}
	if !stopped {
		t.Fatal("uninstall did not stop the pending updater")
	}
	assertUninstallPathsAbsent(t, updateEnvironment.directory, updateEnvironment.unitPath, updateEnvironment.enabledLink,
		agentEnvironment.dataDir, agentEnvironment.unitPath, agentEnvironment.binaryPaths[0], agentEnvironment.binaryPaths[1])
	if err := cancelHostHelper(context.Background(), agentEnvironment.dataDir, updateEnvironment); err != nil {
		t.Fatalf("repeated updater removal: %v", err)
	}
}

func TestUninstallRetainsAgentFilesWhenUpdaterCancellationFails(t *testing.T) {
	agentEnvironment := newAgentUninstallFixture(t)
	updateEnvironment := newHostUpdateCancellationFixture(t, agentEnvironment.dataDir)
	updateEnvironment.run = func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		if name == "systemctl" && arguments[0] == "stop" {
			return nil, errors.New("updater is still running")
		}
		if name == "systemctl" && arguments[0] == "show" {
			return []byte("LoadState=loaded\nActiveState=active\n"), nil
		}
		return nil, nil
	}
	agentEnvironment.pendingUpdate = &updateEnvironment
	agentEnvironment.purgeRuntime = func(context.Context, bool) error {
		t.Fatal("runtime removal continued after updater cancellation failed")
		return nil
	}
	if err := uninstallAgentHostWithEnvironment(context.Background(), true, false, false, agentEnvironment); err == nil {
		t.Fatal("uninstall ignored updater cancellation failure")
	}
	for _, path := range []string{agentEnvironment.dataDir, agentEnvironment.unitPath, agentEnvironment.binaryPaths[0], agentEnvironment.binaryPaths[1]} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("failed cancellation removed Agent files: %s (%v)", path, err)
		}
	}
}

func TestCancelledUpdaterDoesNotActivateRollbackOrCleanAfterRestart(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "agent")
	environment := newHostUpdateCancellationFixture(t, dataDir)
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
		t.Fatal("failed updater stop was ignored")
	}
	operationPath := filepath.Join(environment.directory, "operation.json")
	// Cancellation must be recognized even after partial record loss, before
	// Center requests, executable replacement, rollback, or the stop hook.
	if err := os.Remove(operationPath); err != nil {
		t.Fatal(err)
	}
	if err := runPersistentHostUpdate(context.Background(), operationPath); err != nil {
		t.Fatalf("cancelled updater tried to resume: %v", err)
	}
	if err := cleanPersistentHostUpdate(operationPath); err != nil {
		t.Fatalf("cancelled updater ran its ordinary stop hook: %v", err)
	}
	for _, path := range []string{environment.unitPath, environment.enabledLink, filepath.Join(environment.directory, "vastora"), filepath.Join(environment.directory, "cancelled")} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("cancelled updater changed remaining state: %s (%v)", path, err)
		}
	}
	environment.run = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	if err := cancelHostHelper(context.Background(), dataDir, environment); err != nil {
		t.Fatalf("interrupted updater cancellation did not resume: %v", err)
	}
	assertUninstallPathsAbsent(t, environment.directory, environment.unitPath, environment.enabledLink)
}

func TestUpdateCancellationRejectsAnotherAgentAndUnprotectedMarker(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "agent")
	environment := newHostUpdateCancellationFixture(t, dataDir)
	environment.run = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("foreign update triggered a host command")
		return nil, nil
	}
	if err := cancelHostHelper(context.Background(), filepath.Join(t.TempDir(), "other-agent"), environment); err == nil {
		t.Fatal("uninstall cancelled another Agent's updater")
	}
	operationPath := filepath.Join(environment.directory, "operation.json")
	if err := writeRootFileAtomic(filepath.Join(environment.directory, "cancelled"), []byte("cancelled\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runPersistentHostUpdate(context.Background(), operationPath); err == nil {
		t.Fatal("updater accepted an unprotected cancellation marker")
	}
	if err := cleanPersistentHostUpdate(operationPath); err == nil {
		t.Fatal("update stop hook accepted an unprotected cancellation marker")
	}
}

func newHostUpdateCancellationFixture(t *testing.T, dataDir string) hostHelperCancellationEnvironment {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "update")
	unitPath := filepath.Join(root, "systemd", hostUpdateUnitName)
	enabledLink := filepath.Join(root, "enabled", hostUpdateUnitName)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	operation := hostUpdateOperation{
		Version: 1, TaskID: "agent-update-fixture", Attempt: 1, TargetVersion: "0.1.0-alpha.2", SourceVersion: "0.1.0-alpha.1",
		DataDir: dataDir, Executable: filepath.Join(root, "vastora"), AgentID: "fixture", CenterURL: "http://127.0.0.1:1", Credential: "synthetic-update-credential",
	}
	raw, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string][]byte{
		filepath.Join(directory, "operation.json"): raw,
		filepath.Join(directory, "vastora"):        []byte("staged executable"),
		filepath.Join(directory, "result.json"):    []byte(`{"succeeded":true,"error":""}`),
		unitPath:                                   []byte(hostUpdateServiceUnit()),
	} {
		if err := writeRootFileAtomic(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(enabledLink), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(unitPath, enabledLink); err != nil {
		t.Fatal(err)
	}
	return hostHelperCancellationEnvironment{
		directory: directory, unitName: hostUpdateUnitName, unitPath: unitPath, unitContents: hostUpdateServiceUnit(),
		enabledLink: enabledLink, operationDataDir: hostUpdateDataDir,
		run: func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
	}
}
