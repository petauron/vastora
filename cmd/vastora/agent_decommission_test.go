package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/agentpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestHostDecommissionPersistsResultAcrossCallbackFailure(t *testing.T) {
	environment := newAgentUninstallFixture(t)
	var handoffs, callbacks atomic.Int32
	server := hostControlFixtureServer(&hostControlGRPCFixture{
		beginDecommission: func(ctx context.Context, _ *agentpb.AgentDocument) (*agentpb.Document, error) {
			requireHostFixtureCredential(t, ctx, "synthetic-cleanup-credential")
			handoffs.Add(1)
			return &agentpb.Document{Json: []byte(`{"started":true}`)}, nil
		},
		callback: func(ctx context.Context, request *agentpb.DecommissionRequest) (*agentpb.Document, error) {
			requireHostFixtureCredential(t, ctx, "synthetic-callback-token")
			if request.TaskId != "agent-decommission-fixture" {
				t.Error("cleanup completion did not use its task-bound callback")
				return nil, status.Error(codes.Unauthenticated, "wrong task")
			}
			var result struct {
				Attempt int64 `json:"attempt"`
			}
			if json.Unmarshal(request.Json, &result) != nil || result.Attempt != 2 {
				t.Error("cleanup callback did not contain the successful attempt")
				return nil, status.Error(codes.InvalidArgument, "invalid result")
			}
			for _, path := range append([]string{environment.dataDir, environment.unitPath}, environment.binaryPaths...) {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("callback preceded actual resource removal: %s (%v)", path, err)
					return nil, status.Error(codes.FailedPrecondition, "cleanup incomplete")
				}
			}
			if callbacks.Add(1) == 1 {
				return nil, status.Error(codes.Unavailable, "acknowledgement unavailable")
			}
			return &agentpb.Document{Json: []byte(`{"completed":true}`)}, nil
		},
	})
	defer server.Close()
	operationPath, operation := writeHostDecommissionFixture(t, environment.dataDir, server.URL)
	cleanups := 0
	cleanup := func(ctx context.Context, operation hostDecommissionOperation) error {
		cleanups++
		return uninstallAgentHostWithEnvironment(ctx, operation.DeleteData, false, false, environment)
	}
	client := agent.Client{}
	if err := runHostDecommission(context.Background(), operationPath, client, cleanup); err == nil || !strings.Contains(err.Error(), "report completed host cleanup") {
		t.Fatalf("callback failure was not reported: %v", err)
	}
	resultPath := filepath.Join(filepath.Dir(operationPath), "result.json")
	if found, err := readHostDecommissionResult(resultPath, operation); err != nil || !found {
		t.Fatalf("lost completed cleanup result: %v", err)
	}
	if raw, err := os.ReadFile(resultPath); err != nil || strings.Contains(string(raw), operation.Credential) || strings.Contains(string(raw), operation.CallbackToken) {
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
	if completed, err := protectedCleanupMarkerExists(completionPath, "completed\n"); err != nil || !completed {
		t.Fatalf("acknowledgement was not persisted: %v", err)
	}
}

func TestHostDecommissionRetainsOperationAfterCleanupFailure(t *testing.T) {
	environment := newAgentUninstallFixture(t)
	var callbacks atomic.Int32
	var starts atomic.Int32
	server := hostControlFixtureServer(&hostControlGRPCFixture{
		callback: func(_ context.Context, request *agentpb.DecommissionRequest) (*agentpb.Document, error) {
			callbacks.Add(1)
			var input struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(request.Json, &input); err != nil || input.Error != "host removal interrupted" {
				t.Errorf("failure evidence missing: %v %+v", err, input)
			}
			return &agentpb.Document{Json: []byte(`{"recorded":true}`)}, nil
		},
		beginDecommission: func(context.Context, *agentpb.AgentDocument) (*agentpb.Document, error) {
			if starts.Add(1) > 1 {
				return nil, status.Error(codes.FailedPrecondition, "already started")
			}
			return &agentpb.Document{Json: []byte(`{"started":true}`)}, nil
		},
	})
	defer server.Close()
	operationPath, _ := writeHostDecommissionFixture(t, environment.dataDir, server.URL)
	client := agent.Client{}
	cleanupFailure := errors.New("host removal interrupted")
	if err := runHostDecommission(context.Background(), operationPath, client, func(context.Context, hostDecommissionOperation) error {
		return cleanupFailure
	}); !errors.Is(err, cleanupFailure) {
		t.Fatalf("cleanup failure was not retained: %v", err)
	}
	assertUninstallPathsAbsent(t, filepath.Join(filepath.Dir(operationPath), "result.json"), filepath.Join(filepath.Dir(operationPath), "completed"))
	if callbacks.Load() != 1 {
		t.Fatal("failed cleanup did not send exactly one failure report")
	}
	if _, err := readHostDecommissionOperation(operationPath); err != nil {
		t.Fatalf("failed cleanup lost its recoverable operation: %v", err)
	}
	if err := runHostDecommission(context.Background(), operationPath, client, func(ctx context.Context, operation hostDecommissionOperation) error {
		t.Fatal("failed cleanup resumed with a consumed authorization")
		return nil
	}); err == nil {
		t.Fatal("consumed handoff was accepted")
	}
	for _, path := range []string{environment.dataDir, environment.unitPath, environment.binaryPaths[0], environment.binaryPaths[1]} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("failed helper removed retained state: %v", err)
		}
	}
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
		ExecutionID: "test-execution", SessionID: "test-session",
		Version: 2, TaskID: "agent-decommission-fixture", Attempt: 2, DeleteData: true,
		DataDir: dataDir, AgentID: "fixture", CenterURL: centerURL, Credential: "synthetic-cleanup-credential",
		CallbackURL: centerURL + "/api/v1/agent-decommission-results/agent-decommission-fixture", CallbackToken: "synthetic-callback-token",
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
