package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/petauron/vastora/internal/meridianruntime"
)

// Exercise ApplyMeridianRuntime through the Docker API boundary. The fake
// daemon serves a fixed live container and rejects every mutation endpoint;
// an unexpected replacement cannot pass this test as an observed no-op.
func TestApplyMeridianRevisionOnlyDoesNotMutateDockerContainer(t *testing.T) {
	store := openMeridianLandingTestStore(t)
	ctx := context.Background()
	applied := meridianRecoveryArtifact(7, `{"inbounds":[]}`)
	state := meridianRuntimeState{ApplicationID: "meridian-application", ImageReference: xrayWorkerImageReference, Applied: &applied}
	if err := store.saveMeridianRuntimeState(ctx, state); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(store.dataDir, meridianRuntimeDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "config.json"), applied.Config, 0o600); err != nil {
		t.Fatal(err)
	}
	const containerID = "fixed-meridian-container"
	const startedAt = "2026-09-24T00:00:00Z"
	labels := applicationResourceLabels(meridianKey, "xray", state.ApplicationID, "meridian-runtime-r7")
	labels[xrayWorkerRuntimeLabel] = "xray"
	inspected := container.InspectResponse{
		ID: containerID, Name: "/" + meridianXrayContainer,
		Config: &container.Config{Image: xrayWorkerImageReference, Labels: labels, Cmd: []string{"run", "-c", xrayWorkerConfigPath}},
		State:  &container.State{Running: true, StartedAt: startedAt},
		Mounts: []container.MountPoint{{Type: mount.TypeBind, Source: directory, Destination: filepath.Dir(xrayWorkerConfigPath)}},
	}
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/_ping"):
			w.Header().Set("API-Version", "1.55")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/containers/"+meridianXrayContainer+"/json"):
			_ = json.NewEncoder(w).Encode(inspected)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/containers/"+containerID+"/exec"):
			_, _ = w.Write([]byte(`{"Id":"stats-exec"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/exec/stats-exec/start"):
			// Consume the ExecAttach request body before closing the hijacked
			// socket. Leaving it unread makes the fake server reset the stream
			// while the client reads the Xray response.
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				t.Errorf("read Docker exec request: %v", err)
				return
			}
			conn, writer, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack Docker exec: %v", err)
				return
			}
			defer conn.Close()
			_, _ = writer.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Type: application/vnd.docker.raw-stream\r\n\r\n")
			_, _ = writer.WriteString(`{"stat":[]}` + "\n")
			_ = writer.Flush()
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/exec/stats-exec/json"):
			_, _ = w.Write([]byte(`{"ID":"stats-exec","ContainerID":"fixed-meridian-container","Running":false,"ExitCode":0}`))
		default:
			t.Errorf("unexpected Docker mutation or query: %s %s", r.Method, path)
			http.Error(w, "unexpected Docker endpoint", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	task := meridianruntime.Task{ApplicationID: state.ApplicationID, ImageReference: state.ImageReference, Desired: applied}
	task.Desired.Revision++
	// Docker's ExecAttach dials this host directly. The client accepts an
	// HTTP URL for ordinary requests, but its hijack dialer needs tcp://.
	executor := ApplicationExecutor{Store: store, DockerSocket: "tcp://" + strings.TrimPrefix(server.URL, "http://")}
	result, err := executor.ApplyMeridianRuntime(ctx, task)
	if err != nil || result.Validate(task.Desired) != nil || result.Receipt.Revision != 8 {
		t.Fatalf("no-reload apply receipt=%#v err=%v", result.Receipt, err)
	}
	observed, err := executor.ObserveMeridianRuntime(ctx)
	if err != nil || observed.Receipt.Revision != 8 {
		t.Fatalf("persisted no-reload receipt=%#v err=%v", observed.Receipt, err)
	}
	persisted, err := store.loadMeridianRuntimeState(ctx)
	if err != nil || persisted.Applied.Revision != 8 || persisted.Pending != nil || persisted.HandoverPending {
		t.Fatalf("no-reload journal=%#v err=%v", persisted, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) < 7 {
		t.Fatalf("Docker identity and stats were not observed before and after apply: %v", requests)
	}
	for _, request := range requests {
		if strings.Contains(request, "/images/create") || strings.Contains(request, "/stop") || strings.Contains(request, "/start") && !strings.Contains(request, "/exec/") || strings.Contains(request, "/containers/create") || strings.Contains(request, "/rename") || strings.Contains(request, "/delete") {
			t.Fatalf("same Xray config mutated the fixed container %s started %s: %v", containerID, startedAt, requests)
		}
	}
}
