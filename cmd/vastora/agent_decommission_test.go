package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/petauron/vastora/internal/agent"
)

func TestHostDecommissionPersistsResultAcrossCallbackFailure(t *testing.T) {
	environment := newAgentUninstallFixture(t)
	var handoffs, callbacks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer synthetic-cleanup-credential" {
			t.Error("cleanup callback was not authenticated")
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/decommission/start") {
			handoffs.Add(1)
			_ = json.NewEncoder(response).Encode(map[string]bool{"started": true})
			return
		}
		var result struct {
			Attempt   int64 `json:"attempt"`
			Succeeded bool  `json:"succeeded"`
		}
		if json.NewDecoder(request.Body).Decode(&result) != nil || result.Attempt != 2 || !result.Succeeded {
			t.Error("cleanup callback did not contain the successful attempt")
			http.Error(response, "invalid result", http.StatusBadRequest)
			return
		}
		for _, path := range append([]string{environment.dataDir, environment.unitPath}, environment.binaryPaths...) {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("callback preceded actual resource removal: %s (%v)", path, err)
				http.Error(response, "cleanup incomplete", http.StatusConflict)
				return
			}
		}
		if callbacks.Add(1) == 1 {
			// The result reached Center, but the helper did not receive an
			// acknowledgement. A restarted helper must only replay the result.
			http.Error(response, "acknowledgement unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]bool{"completed": true})
	}))
	defer server.Close()
	operationPath, operation := writeHostDecommissionFixture(t, environment.dataDir, server.URL)
	cleanups := 0
	cleanup := func(ctx context.Context, operation hostDecommissionOperation) error {
		cleanups++
		return uninstallAgentHostWithEnvironment(ctx, operation.DeleteData, false, false, environment)
	}
	client := agent.Client{HTTPClient: server.Client()}
	if err := runHostDecommission(context.Background(), operationPath, client, cleanup); err == nil || !strings.Contains(err.Error(), "report completed host cleanup") {
		t.Fatalf("callback failure was not reported: %v", err)
	}
	resultPath := filepath.Join(filepath.Dir(operationPath), "result.json")
	if found, err := readHostDecommissionResult(resultPath, operation); err != nil || !found {
		t.Fatalf("lost completed cleanup result: %v", err)
	}
	if raw, err := os.ReadFile(resultPath); err != nil || strings.Contains(string(raw), operation.Credential) {
		t.Fatalf("cleanup result is unavailable or contains credentials: %v", err)
	}
	completionPath := filepath.Join(filepath.Dir(operationPath), "completed")
	assertUninstallPathsAbsent(t, completionPath)
	for range 2 {
		if err := runHostDecommission(context.Background(), operationPath, client, cleanup); err != nil {
			t.Fatalf("resume cleanup: %v", err)
		}
	}
	if cleanups != 1 || handoffs.Load() != 1 || callbacks.Load() != 2 {
		t.Fatalf("cleanup was repeated: cleanup=%d handoffs=%d callbacks=%d", cleanups, handoffs.Load(), callbacks.Load())
	}
	if completed, err := protectedCompletionMarkerExists(completionPath); err != nil || !completed {
		t.Fatalf("acknowledgement was not persisted: %v", err)
	}
}

func TestHostDecommissionRetainsOperationAfterCleanupFailure(t *testing.T) {
	environment := newAgentUninstallFixture(t)
	var callbacks atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/result") {
			callbacks.Add(1)
			_ = json.NewEncoder(response).Encode(map[string]bool{"completed": true})
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]bool{"started": true})
	}))
	defer server.Close()
	operationPath, _ := writeHostDecommissionFixture(t, environment.dataDir, server.URL)
	client := agent.Client{HTTPClient: server.Client()}
	cleanupFailure := errors.New("host removal interrupted")
	if err := runHostDecommission(context.Background(), operationPath, client, func(context.Context, hostDecommissionOperation) error {
		return cleanupFailure
	}); !errors.Is(err, cleanupFailure) {
		t.Fatalf("cleanup failure was not retained: %v", err)
	}
	assertUninstallPathsAbsent(t, filepath.Join(filepath.Dir(operationPath), "result.json"), filepath.Join(filepath.Dir(operationPath), "completed"))
	if callbacks.Load() != 0 {
		t.Fatal("failed cleanup sent a completion callback")
	}
	if _, err := readHostDecommissionOperation(operationPath); err != nil {
		t.Fatalf("failed cleanup lost its recoverable operation: %v", err)
	}
	if err := runHostDecommission(context.Background(), operationPath, client, func(ctx context.Context, operation hostDecommissionOperation) error {
		return uninstallAgentHostWithEnvironment(ctx, operation.DeleteData, false, false, environment)
	}); err != nil {
		t.Fatalf("failed cleanup could not resume: %v", err)
	}
	assertUninstallPathsAbsent(t, environment.dataDir, environment.unitPath, environment.binaryPaths[0], environment.binaryPaths[1])
	if callbacks.Load() != 1 {
		t.Fatalf("completion callbacks = %d, want 1", callbacks.Load())
	}
}

func TestHostDecommissionRejectsUntrustedOrStaleResult(t *testing.T) {
	for _, scenario := range []string{"task", "attempt", "permissions", "symlink", "truncated", "unmatched acknowledgement"} {
		t.Run(scenario, func(t *testing.T) {
			environment := newAgentUninstallFixture(t)
			operationPath, operation := writeHostDecommissionFixture(t, environment.dataDir, "http://127.0.0.1:1")
			resultPath := filepath.Join(filepath.Dir(operationPath), "result.json")
			result := hostDecommissionResult{Version: 1, TaskID: operation.TaskID, Attempt: operation.Attempt}
			if scenario == "task" {
				result.TaskID = "agent-decommission-different-agent"
			} else if scenario == "attempt" {
				result.Attempt--
			}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "truncated" {
				raw = raw[:len(raw)/2]
			}
			if scenario == "unmatched acknowledgement" {
				if err := writeRootFileAtomic(filepath.Join(filepath.Dir(operationPath), "completed"), []byte("completed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := writeRootFileAtomic(resultPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if scenario == "permissions" {
				if err := os.Chmod(resultPath, 0o644); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "symlink" {
				if err := os.Rename(resultPath, resultPath+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(resultPath+".original", resultPath); err != nil {
					t.Fatal(err)
				}
			}
			err = runHostDecommission(context.Background(), operationPath, agent.Client{}, func(context.Context, hostDecommissionOperation) error {
				t.Fatal("invalid cleanup result ran destructive cleanup")
				return nil
			})
			if err == nil || strings.Contains(err.Error(), "request Center") {
				t.Fatalf("result was not rejected before networking: %v", err)
			}
		})
	}
}

func writeHostDecommissionFixture(t *testing.T, dataDir, centerURL string) (string, hostDecommissionOperation) {
	t.Helper()
	operation := hostDecommissionOperation{
		Version: 1, TaskID: "agent-decommission-fixture", Attempt: 2, DeleteData: true,
		DataDir: dataDir, AgentID: "fixture", CenterURL: centerURL, Credential: "synthetic-cleanup-credential",
	}
	raw, err := json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "operation.json")
	if err := writeRootFileAtomic(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, operation
}
