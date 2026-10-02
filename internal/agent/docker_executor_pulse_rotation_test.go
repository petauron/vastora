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

// Rotation must inspect all original registrations before its single write.
// No enrollment, delete, arbitrary shell or container-name mutation is accepted.
func TestPulseRotationPinsContainerAndRechecksOriginalNode(t *testing.T) {
	for _, mode := range []string{"success", "old-service", "wrong-owner", "wrong-deployment", "wrong-id", "secret-field", "partial-failure", "trailing-json", "invalid-active", "different-node", "rotation-failed", "bad-token"} {
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
						output = "pulse-service enrollment inspect ID | node rotate ID"
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
						if mode == "different-node" {
							output = strings.ReplaceAll(output, `"node_id":"original-node"`, `"node_id":"another-node"`)
						}
						if mode == "invalid-active" {
							output = strings.ReplaceAll(output, `"node_id":"original-node"`, `"node_id":null`)
						}
						if mode == "secret-field" {
							output = strings.TrimSuffix(output, "}") + `,"token":"must-not-leak"}`
						}
						if mode == "trailing-json" {
							output += "{}"
						}
					} else if slices.Equal(current, []string{"/usr/local/bin/pulse-service", "node", "rotate", "original-node"}) {
						output = "test-rotated-credential-never-publish\n"
						if mode == "bad-token" {
							output = "must-not-leak"
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
					if mode == "rotation-failed" && len(current) == 4 && current[2] == "rotate" {
						code = 1
					}
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
			rotation := pulse.RotationTask{Inspection: task, NodeID: "original-node"}
			result, err := executor.RotatePulse(context.Background(), rotation)
			mu.Lock()
			defer mu.Unlock()
			if mode == "success" {
				if err != nil || result.Validate(rotation) != nil || result.NodeID != "original-node" || len(commands) != 4 {
					t.Fatalf("rotation result invalid: %v", err)
				}
			} else {
				if err == nil || result.Token != "" {
					t.Fatalf("invalid or partial rotation accepted: %v", err)
				}
				if (mode == "rotation-failed" || mode == "bad-token") != taskOutcomeIsUncertain(err) {
					t.Fatal("incorrect uncertainty classification")
				}
				rotations := 0
				for _, args := range commands {
					if len(args) == 4 && args[2] == "rotate" {
						rotations++
					}
				}
				expected := 0
				if mode == "rotation-failed" || mode == "bad-token" {
					expected = 1
				}
				if rotations != expected {
					t.Fatalf("unexpected mutation count %d", rotations)
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
