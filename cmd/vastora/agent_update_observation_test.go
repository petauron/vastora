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
	"testing"

	"github.com/petauron/vastora/internal/agent"
)

func TestHostUpdateObservationIsBoundedAndReadOnly(t *testing.T) {
	for _, mode := range []string{"ready", "pending", "unavailable", "unauthorized", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			observations, reports, waits := 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/result") {
					reports++
					if mode != "ready" || observations != 3 {
						t.Error("result reported before target was observed")
					}
					w.Write([]byte(`{}`))
					return
				}
				var request struct{ Action, SessionID string }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Action != "helper-observe" || request.SessionID != "test-session" {
					t.Errorf("unexpected mutation or malformed observation: %+v %v", request, err)
				}
				observations++
				switch mode {
				case "unavailable":
					w.WriteHeader(http.StatusServiceUnavailable)
				case "unauthorized":
					w.WriteHeader(http.StatusUnauthorized)
				default:
					json.NewEncoder(w).Encode(map[string]bool{"ready": mode == "ready" && observations == 3})
				}
			}))
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
			err = runPersistentHostUpdateWithEnvironment(context.Background(), operationPath, environment, agent.Client{HTTPClient: server.Client()})
			wantObservations, wantReports, wantWaits := 1, 0, 0
			switch mode {
			case "ready":
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
		})
	}
}
