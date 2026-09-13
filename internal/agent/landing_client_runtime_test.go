package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLandingClientHeartbeatIdentityHasBoundedUnixConnections(t *testing.T) {
	directory, err := os.MkdirTemp("", "hb-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	socket := filepath.Join(directory, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var open, accepted atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/localapi/v0/status" || r.URL.Query().Get("peers") != "false" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"BackendState":"Running","Self":{"ID":"self","PublicKey":"node-key","TailscaleIPs":["100.64.0.20"]}}`))
	}))
	server.Listener.Close()
	server.Listener = listener
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			open.Add(1)
			accepted.Add(1)
		}
		if state == http.StateClosed {
			open.Add(-1)
		}
	}
	server.Start()
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.linkChecker.HTTPClient.Transport.(*http.Transport).DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socket)
	}
	if _, err := store.RecordApplied(context.Background(), AppliedInstallation{InstanceID: "runtime-identity", AppKey: threeXUIKey, Version: "test", ServiceAddress: "100.64.0.20", Config: json.RawMessage(`{}`), Secrets: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	store.landingSubscriptionAddress = "100.64.0.20"
	observe := func() {
		t.Helper()
		value := store.observeLandingClientRuntime(context.Background())
		if value == nil || value.Peer.ID != "self" {
			t.Fatalf("heartbeat identity unavailable: %+v", value)
		}
	}
	observe()
	baseline := runtime.NumGoroutine()
	fdBaseline := heartbeatTestResourceSample(t)
	for batch := 0; batch < 5; batch++ {
		for range 100 {
			observe()
		}
		if accepted.Load() != 1 || open.Load() != 1 {
			t.Fatalf("heartbeat pool grew: accepted=%d open=%d", accepted.Load(), open.Load())
		}
		if count := runtime.NumGoroutine(); count > baseline+4 {
			t.Fatalf("heartbeat goroutines grew: baseline=%d now=%d", baseline, count)
		}
		if count := heartbeatTestResourceSample(t); fdBaseline >= 0 && count > fdBaseline+2 {
			t.Fatalf("heartbeat file descriptors grew: baseline=%d now=%d", fdBaseline, count)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for open.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if open.Load() != 0 {
		t.Fatalf("Store.Close retained %d local connections", open.Load())
	}
}

// Report physical process memory without forcing GC. Memory samples are evidence,
// not an assertion that a short fixture proves the complete Agent fits a limit.
func heartbeatTestResourceSample(t *testing.T) int {
	t.Helper()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	t.Logf("goroutines=%d heap_alloc=%d heap_inuse=%d runtime_sys=%d", runtime.NumGoroutine(), memory.HeapAlloc, memory.HeapInuse, memory.Sys)
	if runtime.GOOS != "linux" {
		return -1
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("linux_fd=%d", len(entries))
	data, err := os.ReadFile("/proc/self/smaps_rollup")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Rss:") || strings.HasPrefix(line, "Pss:") || strings.HasPrefix(line, "Swap:") {
			t.Log(line)
		}
	}
	return len(entries)
}
