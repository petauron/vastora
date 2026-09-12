package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/secret"
)

func TestHostUpdateRetainsCandidateAfterActivationCanMigrateSchema(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "bin", "vastora")
	candidate := filepath.Join(root, "update", "vastora")
	dataDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(candidate), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("source"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("target"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := agent.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the schema-15 fixture by removing the tables
	// introduced by schemas 16 through 18. Starting the candidate runs the actual Agent migration,
	// rather than only rewriting the user_version number.
	const sourceSchema = 15
	if agent.CurrentSchemaVersion() != 18 {
		t.Fatal("update this fixture to preserve the complete historical schema-15 shape")
	}
	database, err := sql.Open("sqlite", filepath.Join(dataDir, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DROP TABLE landing_controller_state;
		DROP TABLE landing_runtime_state;
		DROP TABLE node_listener_applied_state;
		PRAGMA user_version = 15;`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	operation := hostUpdateOperation{
		Version: 1, TaskID: "agent-update-schema", Attempt: 1,
		SourceVersion: "0.1.0-alpha.1", TargetVersion: "0.1.0-alpha.2",
		DataDir: dataDir, Executable: executable,
		AgentID: "node", CenterURL: "https://center.example.test", Credential: "synthetic-credential",
	}
	serviceReady := false
	recoveryPrepared := 0
	starts := 0
	environment := hostUpdateActivationEnvironment{
		candidatePath: candidate, recoveryDirectory: filepath.Join(root, "update", hostUpdateRecoveryDirectoryName),
		version: func(_ context.Context, path string) (string, error) {
			raw, err := os.ReadFile(path)
			switch string(raw) {
			case "source":
				return operation.SourceVersion, err
			case "target":
				return operation.TargetVersion, err
			default:
				return "", errors.New("unknown executable")
			}
		},
		run: func(_ context.Context, command string, arguments ...string) ([]byte, error) {
			if command != "systemctl" || len(arguments) == 0 {
				t.Fatalf("unexpected host command: %s %v", command, arguments)
			}
			if arguments[0] == "start" {
				starts++
				installed, err := os.ReadFile(executable)
				if err != nil || string(installed) != "target" {
					t.Fatalf("attempted to restart a source binary with a lower schema ceiling: %q %v", installed, err)
				}
				candidateStore, err := agent.Open(dataDir)
				if err != nil {
					t.Fatalf("candidate could not migrate the old schema: %v", err)
				}
				if err := candidateStore.Close(); err != nil {
					t.Fatal(err)
				}
			}
			return nil, nil
		},
		serviceActive: func(context.Context) bool { return serviceReady },
		wait:          func(context.Context) error { return nil },
		prepareRecovery: func(ctx context.Context, operation hostUpdateOperation, directory string) error {
			recoveryPrepared++
			return prepareHostUpdateRecovery(ctx, operation, directory, candidate)
		},
	}
	activationErr := activateHostUpdate(context.Background(), operation, environment)
	if !errors.Is(activationErr, errHostUpdateCandidatePending) {
		t.Fatalf("candidate activation failure became terminal or rollback-safe: %v", activationErr)
	}
	if _, terminal := hostUpdateActivationResult(activationErr); terminal {
		t.Fatal("candidate activation failure would publish a terminal result and clean its recovery state")
	}
	installed, _ := os.ReadFile(executable)
	previous, _ := os.ReadFile(executable + ".previous")
	if string(installed) != "target" || string(previous) != "source" || recoveryPrepared != 1 {
		t.Fatalf("candidate was rolled back: installed=%q previous=%q recovery=%d", installed, previous, recoveryPrepared)
	}
	if schema := testAgentSchemaVersion(t, filepath.Join(dataDir, "agent.db")); schema != agent.CurrentSchemaVersion() || schema <= sourceSchema {
		t.Fatalf("candidate migration did not exceed the source fixture ceiling: schema=%d source-ceiling=%d", schema, sourceSchema)
	}
	if schema := testAgentSchemaVersion(t, filepath.Join(environment.recoveryDirectory, "agent.db")); schema != sourceSchema {
		t.Fatalf("recovery database was migrated along with production: %d", schema)
	}

	// A restarted helper treats the published recovery manifest as the durable
	// phase marker. It retries only its bound candidate and never prepares or
	// restores source state again.
	serviceReady = true
	if err := activateHostUpdate(context.Background(), operation, environment); err != nil {
		t.Fatalf("candidate activation did not converge after restart: %v", err)
	}
	installed, _ = os.ReadFile(executable)
	if string(installed) != "target" || recoveryPrepared != 1 || starts != 2 {
		t.Fatalf("replay did not retain the candidate: installed=%q recovery=%d starts=%d", installed, recoveryPrepared, starts)
	}
}

func TestHostUpdatePreCommitFailureRestartsSource(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "vastora")
	candidate := filepath.Join(root, "candidate")
	if err := os.WriteFile(executable, []byte("source"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("target"), 0o700); err != nil {
		t.Fatal(err)
	}
	operation := hostUpdateOperation{
		Version: 1, TaskID: "agent-update-precommit", Attempt: 1,
		SourceVersion: "source-version", TargetVersion: "target-version",
		DataDir: filepath.Join(root, "agent"), Executable: executable,
		AgentID: "node", Credential: "synthetic-credential",
	}
	started := 0
	environment := hostUpdateActivationEnvironment{
		candidatePath: candidate, recoveryDirectory: filepath.Join(root, "recovery"),
		version: func(_ context.Context, path string) (string, error) {
			if path == candidate {
				return operation.TargetVersion, nil
			}
			return operation.SourceVersion, nil
		},
		run: func(_ context.Context, _ string, arguments ...string) ([]byte, error) {
			if arguments[0] == "start" {
				started++
			}
			return nil, nil
		},
		serviceActive: func(context.Context) bool { return true },
		wait:          func(context.Context) error { return nil },
		prepareRecovery: func(context.Context, hostUpdateOperation, string) error {
			return errors.New("checkpoint failed")
		},
	}
	err := activateHostUpdate(context.Background(), operation, environment)
	if err == nil || errors.Is(err, errHostUpdateCandidatePending) || !strings.Contains(err.Error(), "checkpoint failed") {
		t.Fatalf("pre-commit recovery failure was not terminal: %v", err)
	}
	installed, _ := os.ReadFile(executable)
	if string(installed) != "source" || started != 1 {
		t.Fatalf("source Agent was not retained and restarted: installed=%q starts=%d", installed, started)
	}
}

func TestHostUpdateRecoveryPointResumesCandidateAfterInterruptedReplacement(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "bin", "vastora")
	candidate := filepath.Join(root, "update", "vastora")
	dataDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(candidate), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("source"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(candidate, []byte("target"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := agent.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	operation := hostUpdateOperation{
		Version: 1, TaskID: "agent-update-interrupted", Attempt: 1,
		SourceVersion: "source-version", TargetVersion: "target-version",
		DataDir: dataDir, Executable: executable, AgentID: "node", Credential: "synthetic-credential",
	}
	recoveryDirectory := filepath.Join(root, "update", hostUpdateRecoveryDirectoryName)
	if err := prepareHostUpdateRecovery(context.Background(), operation, recoveryDirectory, candidate); err != nil {
		t.Fatal(err)
	}
	starts := 0
	environment := hostUpdateActivationEnvironment{
		candidatePath: candidate, recoveryDirectory: recoveryDirectory,
		version: func(_ context.Context, path string) (string, error) {
			raw, err := os.ReadFile(path)
			switch string(raw) {
			case "source":
				return operation.SourceVersion, err
			case "target":
				return operation.TargetVersion, err
			default:
				return "", errors.New("unknown executable")
			}
		},
		run: func(_ context.Context, _ string, arguments ...string) ([]byte, error) {
			if arguments[0] == "start" {
				starts++
			}
			return nil, nil
		},
		serviceActive: func(context.Context) bool { return true },
		wait:          func(context.Context) error { return nil },
		prepareRecovery: func(context.Context, hostUpdateOperation, string) error {
			t.Fatal("replay replaced an already published recovery point")
			return nil
		},
	}
	if err := activateHostUpdate(context.Background(), operation, environment); err != nil {
		t.Fatalf("interrupted replacement did not resume the candidate: %v", err)
	}
	installed, _ := os.ReadFile(executable)
	previous, _ := os.ReadFile(executable + ".previous")
	if string(installed) != "target" || string(previous) != "source" || starts != 1 {
		t.Fatalf("replay did not converge to the candidate: installed=%q previous=%q starts=%d", installed, previous, starts)
	}
}

func TestHostUpdateRecoveryPointIsProtectedVerifiedAndIdempotent(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "agent")
	store, err := agent.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	updateDirectory := filepath.Join(root, "update")
	if err := os.Mkdir(updateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	recoveryDirectory := filepath.Join(updateDirectory, hostUpdateRecoveryDirectoryName)
	candidate := filepath.Join(updateDirectory, "vastora")
	if err := os.WriteFile(candidate, []byte("candidate"), 0o700); err != nil {
		t.Fatal(err)
	}
	operation := hostUpdateOperation{
		Version: 1, TaskID: "agent-update-recovery", Attempt: 1,
		SourceVersion: "0.1.0-alpha.1", TargetVersion: "0.1.0-alpha.2",
		DataDir: dataDir, Executable: filepath.Join(root, "vastora"),
		AgentID: "node", Credential: "synthetic-credential",
	}
	for range 2 {
		if err := prepareHostUpdateRecovery(context.Background(), operation, recoveryDirectory, candidate); err != nil {
			t.Fatalf("prepare recovery point: %v", err)
		}
	}
	for _, change := range []func(*hostUpdateOperation){
		func(value *hostUpdateOperation) { value.TaskID = "agent-update-other" },
		func(value *hostUpdateOperation) { value.Attempt++ },
		func(value *hostUpdateOperation) { value.AgentID = "other-node" },
	} {
		other := operation
		change(&other)
		if err := verifyHostUpdateRecovery(context.Background(), other, recoveryDirectory, candidate); err == nil {
			t.Fatal("a different update task, attempt, or node reused the recovery point")
		}
	}
	for _, path := range []string{
		recoveryDirectory,
		filepath.Join(recoveryDirectory, hostUpdateRecoveryManifestName),
		filepath.Join(recoveryDirectory, hostUpdateRecoveryDatabaseName),
		filepath.Join(recoveryDirectory, hostUpdateRecoveryKeyName),
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if path == recoveryDirectory && info.Mode().Perm() != 0o700 || path != recoveryDirectory && info.Mode().Perm() != 0o600 {
			t.Fatalf("unsafe recovery permissions on %s: %o", path, info.Mode().Perm())
		}
	}
	if err := os.WriteFile(filepath.Join(recoveryDirectory, hostUpdateRecoveryKeyName), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepareHostUpdateRecovery(context.Background(), operation, recoveryDirectory, candidate); err == nil {
		t.Fatal("tampered recovery point was accepted on replay")
	}
}

func testAgentSchemaVersion(t *testing.T, path string) int {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var version int
	if err := database.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func TestHostUpdateRecoveryCleanupRejectsUnexpectedFiles(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "recovery")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	operatorFile := filepath.Join(directory, "operator-file")
	if err := os.WriteFile(operatorFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeHostUpdateRecovery(directory); err == nil {
		t.Fatal("cleanup removed or ignored an unexpected recovery file")
	}
	if _, err := os.Stat(operatorFile); err != nil {
		t.Fatalf("cleanup removed an unowned file: %v", err)
	}
}

func TestHostUpdateRecoveryRejectsMismatchedDatabaseKeyBeforePublication(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "agent")
	store, err := agent.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	otherKey, err := secret.CreateKey(filepath.Join(root, "other.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "agent.key"), otherKey, 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(root, "candidate")
	if err := os.WriteFile(candidate, []byte("target"), 0o700); err != nil {
		t.Fatal(err)
	}
	operation := hostUpdateOperation{TaskID: "agent-update-key", Attempt: 1, AgentID: "node", DataDir: dataDir, SourceVersion: "source", TargetVersion: "target"}
	directory := filepath.Join(root, hostUpdateRecoveryDirectoryName)
	if err := prepareHostUpdateRecovery(context.Background(), operation, directory, candidate); err == nil || !strings.Contains(err.Error(), "database and key do not match") {
		t.Fatalf("unusable recovery pair passed verification: %v", err)
	}
	for _, path := range []string{directory, filepath.Join(root, hostUpdateRecoveryPartialDirectoryName)} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed recovery was published or left staged at %s: %v", path, err)
		}
	}
}

func TestHostUpdateRecoveryReplaysLocallyBeforeCenterIsReachable(t *testing.T) {
	for _, terminalFailure := range []bool{false, true} {
		name := "recovering"
		if terminalFailure {
			name = "precommit_failure"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dataDir := filepath.Join(root, "agent")
			store, err := agent.Open(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			executable := filepath.Join(root, "installed")
			candidate := filepath.Join(root, "candidate")
			for path, contents := range map[string]string{executable: "source", candidate: "target"} {
				if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			starts := 0
			centerCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				centerCalls++
				if r.Header.Get("Authorization") != "Bearer synthetic-credential" {
					t.Error("missing update callback authentication")
				}
				if !terminalFailure && starts != 1 {
					t.Error("helper contacted Center before restoring the local Agent")
				}
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
			}))
			defer server.Close()
			operation := hostUpdateOperation{
				Version: 1, TaskID: "agent-update-offline", Attempt: 1, AgentID: "node",
				DataDir: dataDir, Executable: executable, SourceVersion: "source-version", TargetVersion: "target-version",
				CenterURL: server.URL, Credential: "synthetic-credential",
			}
			operationPath := filepath.Join(root, "operation.json")
			raw, err := json.Marshal(operation)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(operationPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			recovery := filepath.Join(root, hostUpdateRecoveryDirectoryName)
			if err := prepareHostUpdateRecovery(context.Background(), operation, recovery, candidate); err != nil {
				t.Fatal(err)
			}
			if terminalFailure {
				if err := writeHostUpdateResult(filepath.Join(root, "result.json"), hostUpdateResult{Error: "precommit install failed"}); err != nil {
					t.Fatal(err)
				}
			}
			environment := hostUpdateActivationEnvironment{
				candidatePath: candidate, recoveryDirectory: recovery,
				version: func(context.Context, string) (string, error) { return "source-version", nil },
				run: func(_ context.Context, _ string, arguments ...string) ([]byte, error) {
					if arguments[0] == "start" {
						starts++
					}
					return nil, nil
				},
				serviceActive: func(context.Context) bool { return true },
				wait:          func(context.Context) error { return nil },
			}
			client := agent.Client{HTTPClient: server.Client()}
			if err := runPersistentHostUpdateWithEnvironment(context.Background(), operationPath, environment, client); err == nil {
				t.Fatal("unreachable Center was reported as a completed update")
			}
			installed, err := os.ReadFile(executable)
			want := "target"
			if terminalFailure {
				want = "source"
			}
			if err != nil || string(installed) != want || centerCalls != 1 || (terminalFailure && starts != 0) {
				t.Fatalf("offline replay: installed=%q starts=%d centerCalls=%d err=%v", installed, starts, centerCalls, err)
			}
			if _, err := os.Stat(filepath.Join(recovery, "agent.db")); err != nil {
				t.Fatal("unacknowledged recovery lost its database copy")
			}
			if _, err := os.Stat(filepath.Join(root, "completed")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unacknowledged recovery published a completion marker")
			}
		})
	}
}
