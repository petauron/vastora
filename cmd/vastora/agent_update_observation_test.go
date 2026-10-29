package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/agentpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestHostUpdateObservationIsBoundedAndReadOnly(t *testing.T) {
	for _, mode := range []string{"ready", "pending", "unavailable", "unauthorized", "cancelled", "result-unavailable", "result-unauthorized", "result-conflict"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			observations, reports, waits := 0, 0, 0
			targetReady := mode == "ready" || strings.HasPrefix(mode, "result-")
			server := hostControlFixtureServer(&hostControlGRPCFixture{
				complete: func(_ context.Context, _ *agentpb.TaskDocument) (*agentpb.Document, error) {
					reports++
					if !targetReady || observations != 3 {
						t.Error("result reported before target was observed")
					}
					if code := map[string]codes.Code{"result-unavailable": codes.Unavailable, "result-unauthorized": codes.Unauthenticated, "result-conflict": codes.FailedPrecondition}[mode]; code != codes.OK {
						return nil, status.Error(code, "result rejected")
					}
					return &agentpb.Document{Json: []byte(`{}`)}, nil
				},
				transition: func(_ context.Context, input *agentpb.ExecutionDocument) (*agentpb.Document, error) {
					var request struct{ Action, SessionID string }
					if err := json.Unmarshal(input.Json, &request); err != nil || request.Action != "helper-observe" || request.SessionID != "test-session" {
						t.Errorf("unexpected mutation or malformed observation: %+v %v", request, err)
					}
					observations++
					switch mode {
					case "unavailable":
						return nil, status.Error(codes.Unavailable, "unavailable")
					case "unauthorized":
						return nil, status.Error(codes.Unauthenticated, "unauthorized")
					default:
						body, err := json.Marshal(map[string]bool{"ready": targetReady && observations == 3})
						return &agentpb.Document{Json: body}, err
					}
				},
			})
			defer server.Close()
			operation := hostUpdateOperation{Version: 1, TaskID: "agent-update-test", Attempt: 1, AgentID: "node", ExecutionID: "test-execution", SessionID: "test-session", DataDir: root, Executable: filepath.Join(root, "vastora"), SourceVersion: "source", TargetVersion: "target", CenterURL: server.URL, Credential: "synthetic-credential"}
			raw, err := json.Marshal(operation)
			if err != nil {
				t.Fatal(err)
			}
			operationPath := filepath.Join(root, "operation.json")
			if err := os.WriteFile(operationPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := writeHostUpdateResult(filepath.Join(root, filepath.Base(hostUpdateResultPath)), hostUpdateResult{Succeeded: true}); err != nil {
				t.Fatal(err)
			}
			environment := hostUpdateActivationEnvironment{
				run: func(context.Context, string, ...string) ([]byte, error) {
					t.Error("observation repeated a service command")
					return nil, errors.New("unexpected mutation")
				},
				wait: func(context.Context) error {
					waits++
					if mode == "cancelled" {
						return context.Canceled
					}
					return nil
				},
			}
			err = runPersistentHostUpdateWithEnvironment(context.Background(), operationPath, environment, agent.Client{})
			wantObservations, wantReports, wantWaits := 1, 0, 0
			switch mode {
			case "ready", "result-unavailable", "result-unauthorized", "result-conflict":
				wantObservations, wantReports, wantWaits = 3, 1, 2
			case "pending":
				wantObservations, wantWaits = 30, 30
			case "cancelled":
				wantWaits = 1
			}
			if (err == nil) != (mode == "ready") || observations != wantObservations || reports != wantReports || waits != wantWaits {
				t.Fatalf("observations=%d reports=%d waits=%d err=%v", observations, reports, waits, err)
			}
			_, markerErr := os.Stat(filepath.Join(root, filepath.Base(hostUpdateCompleted)))
			if (markerErr == nil) != (mode == "ready") {
				t.Fatalf("completion marker: %v", markerErr)
			}
			result, exists, err := readHostUpdateResult(filepath.Join(root, filepath.Base(hostUpdateResultPath)))
			if err != nil || !exists || !result.Succeeded {
				t.Fatalf("local activation evidence lost: %+v %v", result, err)
			}
		})
	}
}
