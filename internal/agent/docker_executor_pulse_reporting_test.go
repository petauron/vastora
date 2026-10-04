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

func TestPulseReportingPinsServiceAndUsesSecretStdin(t *testing.T) {
	for _, mode := range []string{"fresh", "retained", "unsupported", "wrong-deployment", "wrong-node", "secret-field", "trailing-json", "failed"} {
		t.Run(mode, func(t *testing.T) {
			task := pulse.ReportingTask{ApplicationID: "monitor-service", DeploymentID: "reviewed", Credentials: pulse.RestoreCredentials{NodeID: "11111111-1111-4111-8111-111111111111", Token: "test-rotated-credential-never-publish"}}
			labels := applicationResourceLabels(pulse.ServiceKey, "pulse", task.ApplicationID, task.DeploymentID)
			if mode == "wrong-deployment" {
				labels[applicationDeploymentIDLabel] = "other"
			}
			var mu sync.Mutex
			var cmd []string
			reads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch {
				case strings.HasSuffix(r.URL.Path, "/_ping"):
					w.Header().Set("API-Version", "1.55")
				case strings.HasSuffix(r.URL.Path, "/containers/"+pulseContainer+"/json"):
					_ = json.NewEncoder(w).Encode(container.InspectResponse{ID: "fixed-pulse", Config: &container.Config{Labels: labels}, State: &container.State{Running: true}})
				case strings.HasSuffix(r.URL.Path, "/containers/fixed-pulse/exec"):
					var input struct {
						Cmd         []string
						Env         []string
						AttachStdin bool
					}
					_ = json.NewDecoder(r.Body).Decode(&input)
					cmd = input.Cmd
					if strings.Contains(strings.Join(cmd, " ")+strings.Join(input.Env, " "), task.Credentials.Token) || input.AttachStdin != (len(cmd) == 4) {
						t.Error("credential escaped stdin")
					}
					_, _ = w.Write([]byte(`{"Id":"read-exec"}`))
				case strings.HasSuffix(r.URL.Path, "/exec/read-exec/start"):
					_, _ = io.Copy(io.Discard, r.Body)
					conn, writer, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					_, _ = writer.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Type: application/vnd.docker.raw-stream\r\n\r\n")
					_ = writer.Flush()
					output := "node reporting ID"
					if len(cmd) == 4 {
						if !slices.Equal(cmd, []string{"/usr/local/bin/pulse-service", "node", "reporting", task.Credentials.NodeID}) {
							t.Error("unexpected command")
						}
						input, _ := io.ReadAll(conn)
						if string(input) != task.Credentials.Token+"\n" {
							t.Error("credential input mismatch")
						}
						reads++
						output = `{"node_id":"11111111-1111-4111-8111-111111111111","observed_at_unix_ms":3000,"rotated_at_unix_ms":1000,"last_seen_at_unix_ms":2000}`
						if mode == "retained" {
							output = strings.ReplaceAll(output, `:2000`, `:999`)
						}
						if mode == "wrong-node" {
							output = strings.ReplaceAll(output, task.Credentials.NodeID, "other")
						}
						if mode == "secret-field" {
							output = strings.TrimSuffix(output, "}") + `,"token":"must-not-leak"}`
						}
						if mode == "trailing-json" {
							output += "{}"
						}
					} else if mode == "unsupported" {
						output = "enrollment create"
					}
					header := make([]byte, 8)
					header[0] = 1
					binary.BigEndian.PutUint32(header[4:], uint32(len(output)))
					_, _ = writer.Write(header)
					_, _ = writer.WriteString(output)
					_ = writer.Flush()
				case strings.HasSuffix(r.URL.Path, "/exec/read-exec/json"):
					code := 0
					if mode == "failed" && len(cmd) == 4 {
						code = 1
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Running": false, "ExitCode": code})
				default:
					t.Errorf("unexpected Docker request %s", r.URL.Path)
				}
			}))
			defer server.Close()
			result, err := (ApplicationExecutor{PackageStateDirectory: pulseReviewedTestPackage(t, task.ApplicationID, task.DeploymentID), DockerSocket: "tcp://" + strings.TrimPrefix(server.URL, "http://")}).InspectPulseReporting(context.Background(), task)
			mu.Lock()
			defer mu.Unlock()
			if mode == "fresh" || mode == "retained" {
				if err != nil || reads != 1 || result.FreshAfterRotation(task) != (mode == "fresh") {
					t.Fatalf("unexpected reporting evidence: %v", err)
				}
			} else if err == nil || strings.Contains(err.Error(), task.Credentials.Token) || strings.Contains(err.Error(), "must-not-leak") {
				t.Fatal("invalid or secret-bearing response accepted")
			}
		})
	}
}
