package agent

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/petauron/vastora/internal/pulse"
)

// All allowed API calls are read-only Docker inspection and fixed Pulse CLI
// reads. A create/revoke/import/upgrade or arbitrary shell endpoint fails here.
func TestPulseInspectionUsesReviewedContainerAndFixedCLI(t *testing.T) {
	for _, mode := range []string{"success", "old-service", "wrong-owner", "wrong-deployment", "wrong-id", "secret-field", "partial-failure", "trailing-json", "invalid-active"} {
		t.Run(mode, func(t *testing.T) {
			task := pulse.InspectionTask{ApplicationID: "monitor-service", DeploymentID: "reviewed-deployment", EnrollmentIDs: []string{"registration-1", "registration-2"}}
			labels := applicationResourceLabels(pulse.ServiceKey, "pulse", task.ApplicationID, task.DeploymentID)
			if mode == "wrong-owner" {
				labels[applicationInstallationLabel] = "other-app"
			}
			if mode == "wrong-deployment" {
				labels[applicationDeploymentIDLabel] = "other-deployment"
			}
			var mu sync.Mutex
			var commands [][]string
			var current []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				path := r.URL.Path
				switch {
				case strings.HasSuffix(path, "/_ping"):
					w.Header().Set("API-Version", "1.55")
					w.WriteHeader(http.StatusOK)
				case r.Method == http.MethodGet && strings.HasSuffix(path, "/containers/"+pulseContainer+"/json"):
					_ = json.NewEncoder(w).Encode(container.InspectResponse{ID: "fixed-pulse", Config: &container.Config{Labels: labels}, State: &container.State{Running: true}})
				case r.Method == http.MethodPost && strings.HasSuffix(path, "/containers/fixed-pulse/exec"):
					var input struct{ Cmd []string }
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Error(err)
					}
					current = input.Cmd
					commands = append(commands, slices.Clone(current))
					_, _ = w.Write([]byte(`{"Id":"inspection-exec"}`))
				case r.Method == http.MethodPost && strings.HasSuffix(path, "/exec/inspection-exec/start"):
					_, _ = io.Copy(io.Discard, r.Body)
					conn, writer, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					_, _ = writer.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Type: application/vnd.docker.raw-stream\r\n\r\n")
					output := ""
					if slices.Equal(current, []string{"/usr/local/bin/pulse-service", "--help"}) {
						output = "pulse-service enrollment inspect ID"
						if mode == "old-service" {
							output = "pulse-service enrollment create"
						}
					} else if len(current) == 4 && slices.Equal(current[:3], []string{"/usr/local/bin/pulse-service", "enrollment", "inspect"}) && slices.Contains(task.EnrollmentIDs, current[3]) {
						id := current[3]
						if mode == "wrong-id" {
							id = "not-reviewed"
						}
						raw, _ := json.Marshal(map[string]any{"id": id, "expires_at_unix_ms": 100, "consumed_at_unix_ms": 90, "node_id": "original-node", "node_active": true})
						output = string(raw)
						if mode == "invalid-active" {
							output = strings.ReplaceAll(output, `"node_id":"original-node"`, `"node_id":null`)
						}
						if mode == "secret-field" {
							output = strings.TrimSuffix(output, "}") + `,"token":"must-not-leak"}`
						}
						if mode == "trailing-json" {
							output += "{}"
						}
					} else {
						t.Errorf("unexpected CLI command: %v", current)
					}
					header := make([]byte, 8)
					header[0] = 1
					binary.BigEndian.PutUint32(header[4:], uint32(len(output)))
					_, _ = writer.Write(header)
					_, _ = writer.WriteString(output)
					_ = writer.Flush()
				case r.Method == http.MethodGet && strings.HasSuffix(path, "/exec/inspection-exec/json"):
					code := 0
					if mode == "partial-failure" && len(current) == 4 && current[3] == "registration-2" {
						code = 1
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"ID": "inspection-exec", "Running": false, "ExitCode": code})
				default:
					t.Errorf("unexpected Docker mutation or query: %s %s", r.Method, path)
					http.Error(w, "not allowed", 500)
				}
			}))
			defer server.Close()
			executor := ApplicationExecutor{DockerSocket: "tcp://" + strings.TrimPrefix(server.URL, "http://")}
			result, err := executor.InspectPulse(context.Background(), task)
			mu.Lock()
			defer mu.Unlock()
			if mode == "success" {
				id, identityErr := result.OriginalNodeID(task)
				if err != nil || identityErr != nil || id != "original-node" || len(commands) != 3 {
					t.Fatalf("inspection result: %+v %v %v", result, err, identityErr)
				}
			} else {
				if err == nil || len(result.Records) != 0 {
					t.Fatalf("invalid or partial inspection accepted: %+v %v", result, err)
				}
				if strings.Contains(err.Error(), "must-not-leak") {
					t.Fatal("response secret leaked")
				}
				if (mode == "wrong-owner" || mode == "wrong-deployment") && len(commands) != 0 {
					t.Fatal("executed against unreviewed container")
				}
				if mode == "old-service" && len(commands) != 1 {
					t.Fatal("unsupported service received a command")
				}
			}
		})
	}
}
