package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/agentpb"
	"github.com/petauron/vastora/internal/gateway"
)

type fakeCaddyRuntimeInspector struct {
	mu       sync.Mutex
	identity string
	err      error
	calls    int
}

func (inspector *fakeCaddyRuntimeInspector) ProtectedSystemServices(context.Context) ([]string, error) {
	return nil, nil
}

func (inspector *fakeCaddyRuntimeInspector) Health(ctx context.Context) error {
	_, err := inspector.runtimeHealthIdentity(ctx)
	return err
}

func (inspector *fakeCaddyRuntimeInspector) runtimeHealthIdentity(context.Context) (string, error) {
	inspector.mu.Lock()
	defer inspector.mu.Unlock()
	inspector.calls++
	if inspector.err != nil {
		return "", inspector.err
	}
	return inspector.identity, nil
}

func (inspector *fakeCaddyRuntimeInspector) set(identity string, err error) {
	inspector.mu.Lock()
	inspector.identity = identity
	inspector.err = err
	inspector.mu.Unlock()
}

func (inspector *fakeCaddyRuntimeInspector) callCount() int {
	inspector.mu.Lock()
	defer inspector.mu.Unlock()
	return inspector.calls
}

type fakeCaddyHealthOnlyInspector struct {
	mu    sync.Mutex
	calls int
}

func (inspector *fakeCaddyHealthOnlyInspector) ProtectedSystemServices(context.Context) ([]string, error) {
	return nil, nil
}

func (inspector *fakeCaddyHealthOnlyInspector) Health(context.Context) error {
	inspector.mu.Lock()
	defer inspector.mu.Unlock()
	inspector.calls++
	return nil
}

func (inspector *fakeCaddyHealthOnlyInspector) callCount() int {
	inspector.mu.Lock()
	defer inspector.mu.Unlock()
	return inspector.calls
}

type fakeCaddyAdminLog struct {
	level  string
	method string
	uri    string
}

type fakeCaddyAdminFixture struct {
	mu                 sync.Mutex
	config             []byte
	getStatus          int
	loadStatus         int
	metricsStatus      int
	metricsOverride    *string
	reloadSuccessful   int
	reloadTimestamp    float64
	pinReloadTimestamp bool
	processStartTime   float64
	requestCounts      map[string]int
	requestErrors      map[string]int
	getCalls           int
	loadCalls          int
	metricsCalls       int
	logs               []fakeCaddyAdminLog
	onConfigGet        func(*fakeCaddyAdminFixture)
	socketPath         string
	listener           net.Listener
	server             *http.Server
	httpServer         *httptest.Server
}

func newCaddyAdminUnixFixture(t *testing.T) (*fakeCaddyAdminFixture, *CaddyGatewayDriver) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "vastora-caddy-hb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "admin.sock")
	fixture := newFakeCaddyAdminState(socketPath)
	fixture.startUnix(t)
	t.Cleanup(fixture.close)
	driver, err := NewCaddyGatewayDriver("unix://" + socketPath)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, driver
}

func newCaddyAdminHTTPFixture(t *testing.T) (*fakeCaddyAdminFixture, *CaddyGatewayDriver) {
	t.Helper()
	fixture := newFakeCaddyAdminState("")
	fixture.httpServer = httptest.NewServer(http.HandlerFunc(fixture.handle))
	t.Cleanup(fixture.httpServer.Close)
	driver, err := NewCaddyGatewayDriver(fixture.httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, driver
}

func newFakeCaddyAdminState(socketPath string) *fakeCaddyAdminFixture {
	return &fakeCaddyAdminFixture{
		config:           []byte(`{"admin":{"config":{"persist":false}}}`),
		getStatus:        http.StatusOK,
		loadStatus:       http.StatusOK,
		metricsStatus:    http.StatusOK,
		reloadSuccessful: 1,
		reloadTimestamp:  1795780000.125,
		processStartTime: 1795779900.5,
		requestCounts:    map[string]int{},
		requestErrors:    map[string]int{},
		socketPath:       socketPath,
	}
}

func (fixture *fakeCaddyAdminFixture) startUnix(t *testing.T) {
	t.Helper()
	listener, err := net.Listen("unix", fixture.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(fixture.handle)}
	fixture.mu.Lock()
	fixture.listener = listener
	fixture.server = server
	fixture.mu.Unlock()
	go func() { _ = server.Serve(listener) }()
}

func (fixture *fakeCaddyAdminFixture) replaceUnixSocketGraceful(t *testing.T) {
	t.Helper()
	fixture.mu.Lock()
	oldServer := fixture.server
	oldListener := fixture.listener
	retired := fixture.socketPath + ".old"
	fixture.mu.Unlock()
	// Keep the old inode referenced under a sibling name before recreating the
	// socket so the replacement allocates a distinct inode, while allowing any
	// in-flight HTTP response on oldServer to complete cleanly.
	if err := os.Rename(fixture.socketPath, retired); err != nil {
		t.Fatal(err)
	}
	if oldListener != nil {
		if unixListener, ok := oldListener.(*net.UnixListener); ok {
			unixListener.SetUnlinkOnClose(false)
		}
		_ = oldListener.Close()
	}
	if oldServer != nil {
		t.Cleanup(func() { _ = oldServer.Close() })
	}
	fixture.startUnix(t)
}

func (fixture *fakeCaddyAdminFixture) replaceUnixSocket(t *testing.T) {
	t.Helper()
	fixture.mu.Lock()
	oldServer := fixture.server
	oldListener := fixture.listener
	retired := fixture.socketPath + ".old"
	fixture.mu.Unlock()
	// Keep the old inode referenced under a sibling name before recreating the
	// socket so the replacement is guaranteed to allocate a distinct inode.
	if err := os.Rename(fixture.socketPath, retired); err != nil {
		t.Fatal(err)
	}
	if oldServer != nil {
		_ = oldServer.Close()
	}
	if oldListener != nil {
		_ = oldListener.Close()
	}
	fixture.startUnix(t)
}

func (fixture *fakeCaddyAdminFixture) closeListenerKeepSocket(t *testing.T) {
	t.Helper()
	fixture.mu.Lock()
	oldServer := fixture.server
	oldListener := fixture.listener
	fixture.server = nil
	fixture.listener = nil
	fixture.mu.Unlock()
	if unixListener, ok := oldListener.(*net.UnixListener); ok {
		unixListener.SetUnlinkOnClose(false)
	}
	if oldServer != nil {
		_ = oldServer.Close()
	}
	if oldListener != nil {
		_ = oldListener.Close()
	}
}

func (fixture *fakeCaddyAdminFixture) removeUnixSocket(t *testing.T) {
	t.Helper()
	fixture.mu.Lock()
	oldServer := fixture.server
	oldListener := fixture.listener
	fixture.server = nil
	fixture.listener = nil
	fixture.mu.Unlock()
	if oldServer != nil {
		_ = oldServer.Close()
	}
	if oldListener != nil {
		_ = oldListener.Close()
	}
	if err := os.Remove(fixture.socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func (fixture *fakeCaddyAdminFixture) close() {
	fixture.mu.Lock()
	server := fixture.server
	listener := fixture.listener
	fixture.server = nil
	fixture.listener = nil
	fixture.mu.Unlock()
	if server != nil {
		_ = server.Close()
	}
	if listener != nil {
		_ = listener.Close()
	}
}

func (fixture *fakeCaddyAdminFixture) handle(w http.ResponseWriter, r *http.Request) {
	fixture.mu.Lock()
	level := "info"
	if r.RequestURI == "/metrics" {
		level = "debug"
	}
	fixture.logs = append(fixture.logs, fakeCaddyAdminLog{level: level, method: r.Method, uri: r.RequestURI})

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/metrics":
		fixture.metricsCalls++
		status := fixture.metricsStatus
		var body string
		if fixture.metricsOverride != nil {
			body = *fixture.metricsOverride
		} else {
			body = fixture.renderMetricsLocked()
		}
		fixture.recordAdminRequestLocked("/metrics", r.Method, status)
		fixture.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)

	case r.Method == http.MethodGet && r.URL.Path == "/config/":
		fixture.getCalls++
		status := fixture.getStatus
		payload := append([]byte(nil), fixture.config...)
		hook := fixture.onConfigGet
		fixture.onConfigGet = nil
		fixture.recordAdminRequestLocked("/config/", r.Method, status)
		fixture.mu.Unlock()
		if hook != nil {
			hook(fixture)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(payload)

	case r.Method == http.MethodPost && r.URL.Path == "/load":
		fixture.loadCalls++
		body, _ := io.ReadAll(r.Body)
		status := fixture.loadStatus
		if status >= 200 && status < 300 && !json.Valid(body) {
			status = http.StatusBadRequest
		}
		if status >= 200 && status < 300 {
			fixture.config = body
			fixture.reloadSuccessful = 1
			if !fixture.pinReloadTimestamp {
				fixture.reloadTimestamp += 1.0
			}
		} else {
			fixture.reloadSuccessful = 0
			fixture.logs = append(fixture.logs, fakeCaddyAdminLog{level: "error", method: r.Method, uri: r.RequestURI})
			fixture.recordAdminErrorLocked("/load", r.Method)
		}
		fixture.recordAdminRequestLocked("/load", r.Method, status)
		fixture.mu.Unlock()
		w.WriteHeader(status)

	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/config/"):
		fixture.config = []byte(`{"admin":{"config":{"persist":false}}}`)
		fixture.reloadSuccessful = 1
		if !fixture.pinReloadTimestamp {
			fixture.reloadTimestamp += 1.0
		}
		fixture.recordAdminRequestLocked("/config/", r.Method, http.StatusOK)
		fixture.mu.Unlock()
		w.WriteHeader(http.StatusOK)

	default:
		fixture.mu.Unlock()
		http.NotFound(w, r)
	}
}

func (fixture *fakeCaddyAdminFixture) recordAdminRequestLocked(path, method string, status int) {
	key := fmt.Sprintf(`code="%d",handler="admin",method="%s",path="%s"`, status, method, path)
	fixture.requestCounts[key]++
}

func (fixture *fakeCaddyAdminFixture) recordAdminErrorLocked(path, method string) {
	key := fmt.Sprintf(`handler="admin",method="%s",path="%s"`, method, path)
	fixture.requestErrors[key]++
}

func (fixture *fakeCaddyAdminFixture) renderMetricsLocked() string {
	var b strings.Builder
	b.WriteString("# HELP caddy_config_last_reload_successful Whether the last configuration reload attempt was successful.\n")
	b.WriteString("# TYPE caddy_config_last_reload_successful gauge\n")
	fmt.Fprintf(&b, "caddy_config_last_reload_successful %d\n", fixture.reloadSuccessful)
	b.WriteString("# HELP caddy_config_last_reload_success_timestamp_seconds Timestamp of the last successful configuration reload.\n")
	b.WriteString("# TYPE caddy_config_last_reload_success_timestamp_seconds gauge\n")
	fmt.Fprintf(&b, "caddy_config_last_reload_success_timestamp_seconds %.6f\n", fixture.reloadTimestamp)
	fmt.Fprintf(&b, "process_start_time_seconds %.6f\n", fixture.processStartTime)
	for labels, count := range fixture.requestCounts {
		fmt.Fprintf(&b, "caddy_admin_http_requests_total{%s} %d\n", labels, count)
	}
	for labels, count := range fixture.requestErrors {
		fmt.Fprintf(&b, "caddy_admin_http_request_errors_total{%s} %d\n", labels, count)
	}
	return b.String()
}

func (fixture *fakeCaddyAdminFixture) setConfig(raw string) {
	fixture.mu.Lock()
	fixture.config = []byte(raw)
	fixture.mu.Unlock()
}

func (fixture *fakeCaddyAdminFixture) setGetStatus(status int) {
	fixture.mu.Lock()
	fixture.getStatus = status
	fixture.mu.Unlock()
}

func (fixture *fakeCaddyAdminFixture) setLoadStatus(status int) {
	fixture.mu.Lock()
	fixture.loadStatus = status
	fixture.mu.Unlock()
}

func (fixture *fakeCaddyAdminFixture) setMetricsStatus(status int) {
	fixture.mu.Lock()
	fixture.metricsStatus = status
	fixture.mu.Unlock()
}

func (fixture *fakeCaddyAdminFixture) setMetricsOverride(raw *string) {
	fixture.mu.Lock()
	fixture.metricsOverride = raw
	fixture.mu.Unlock()
}

func (fixture *fakeCaddyAdminFixture) setPinReloadTimestamp(pin bool) {
	fixture.mu.Lock()
	fixture.pinReloadTimestamp = pin
	fixture.mu.Unlock()
}

func (fixture *fakeCaddyAdminFixture) setOnConfigGet(hook func(*fakeCaddyAdminFixture)) {
	fixture.mu.Lock()
	fixture.onConfigGet = hook
	fixture.mu.Unlock()
}

func (fixture *fakeCaddyAdminFixture) simulateExternalReload(raw string) {
	fixture.mu.Lock()
	fixture.config = []byte(raw)
	fixture.reloadSuccessful = 1
	if !fixture.pinReloadTimestamp {
		fixture.reloadTimestamp += 1.0
	}
	fixture.recordAdminRequestLocked("/load", http.MethodPost, http.StatusOK)
	fixture.logs = append(fixture.logs, fakeCaddyAdminLog{level: "info", method: http.MethodPost, uri: "/load"})
	fixture.mu.Unlock()
}

func (fixture *fakeCaddyAdminFixture) counts() (gets, loads, metrics int) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.getCalls, fixture.loadCalls, fixture.metricsCalls
}

func (fixture *fakeCaddyAdminFixture) logCounts() (infoReceived, debugMetrics, infoWrites, errors int) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	for _, entry := range fixture.logs {
		switch entry.level {
		case "info":
			infoReceived++
			if entry.method != http.MethodGet {
				infoWrites++
			}
		case "debug":
			if entry.uri == "/metrics" {
				debugMetrics++
			}
		case "error":
			errors++
		}
	}
	return infoReceived, debugMetrics, infoWrites, errors
}

func newHeartbeatObservationFixture(t *testing.T, store *Store) chan bool {
	t.Helper()
	observed := make(chan bool, 32)
	server := controlFixtureServer(&controlGRPCFixture{heartbeat: func(_ context.Context, input *agentpb.AgentDocument) (*agentpb.Document, error) {
		var body struct {
			GatewayHealthy bool `json:"gatewayHealthy"`
		}
		if err := json.Unmarshal(input.Json, &body); err != nil {
			t.Error(err)
		}
		observed <- body.GatewayHealthy
		return fixtureDocument(t, map[string]any{}), nil
	}})
	t.Cleanup(server.Close)
	if err := store.SaveConnection(context.Background(), testConnection(t, "agent-1", "test", server.URL, "credential")); err != nil {
		t.Fatal(err)
	}
	return observed
}

func TestCaddyHeartbeatSteadyStateProducesZeroInfoLogs(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	observed := newHeartbeatObservationFixture(t, store)

	fixture, caddy := newCaddyAdminUnixFixture(t)
	inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
	caddy.SystemGateway = inspector

	managed := &ManagedGatewayDriver{Caddy: caddy, Layer4: &fakeLayer4Provisioner{}, Runtime: &fakeGatewayRuntimeProvisioner{}}
	if err := applyGatewayDesiredState(ctx, store, managed, gatewayState(1, 3000), nil); err != nil {
		t.Fatal(err)
	}
	gets, loads, metrics := fixture.counts()
	if gets != 1 || loads != 1 || metrics != 0 {
		t.Fatalf("post-apply strict verification counts = gets:%d loads:%d metrics:%d, want 1, 1, 0", gets, loads, metrics)
	}

	agentClient := Client{GatewayDriver: managed}
	// Strict StartupHeartbeat runs GET /config/ and never touches /metrics.
	if err := agentClient.StartupHeartbeat(ctx, store); err != nil {
		t.Fatal(err)
	}
	if healthy := <-observed; !healthy {
		t.Fatal("startup heartbeat reported unhealthy gateway")
	}
	gets, _, metrics = fixture.counts()
	if gets != 2 || metrics != 0 {
		t.Fatalf("StartupHeartbeat did not stay strictly on GET /config/: gets=%d metrics=%d want 2 and 0", gets, metrics)
	}

	// First routine heartbeat seeds the baseline via /metrics -> /config/ ->
	// /metrics (one warm-up GET /config/ INFO log).
	if err := agentClient.Heartbeat(ctx, store); err != nil {
		t.Fatal(err)
	}
	if healthy := <-observed; !healthy {
		t.Fatal("warm-up routine heartbeat reported unhealthy gateway")
	}
	getsBefore, _, metricsBefore := fixture.counts()
	if getsBefore != 3 || metricsBefore != 2 {
		t.Fatalf("warm-up routine heartbeat counts = gets:%d metrics:%d, want 3 and 2", getsBefore, metricsBefore)
	}
	infoBefore, debugBefore, _, _ := fixture.logCounts()
	runtimeBefore := inspector.callCount()

	// Subsequent steady-state routine heartbeats must produce zero admin.api
	// INFO logs and zero GET /config/ reads while checking runtime health and
	// /metrics (DEBUG) on every heartbeat.
	const steadyHeartbeats = 10
	for range steadyHeartbeats {
		if err := agentClient.Heartbeat(ctx, store); err != nil {
			t.Fatal(err)
		}
		if healthy := <-observed; !healthy {
			t.Fatal("steady-state heartbeat reported unhealthy gateway")
		}
	}
	getsAfter, _, metricsAfter := fixture.counts()
	infoAfter, debugAfter, _, _ := fixture.logCounts()
	if getsAfter != getsBefore {
		t.Fatalf("steady-state heartbeats ran GET /config/: before=%d after=%d", getsBefore, getsAfter)
	}
	if infoAfter != infoBefore {
		t.Fatalf("steady-state heartbeats emitted admin.api INFO logs: before=%d after=%d", infoBefore, infoAfter)
	}
	if metricsAfter-metricsBefore != steadyHeartbeats || debugAfter-debugBefore != steadyHeartbeats {
		t.Fatalf("steady-state heartbeats did not verify /metrics each tick: metrics=%d debug=%d want %d", metricsAfter-metricsBefore, debugAfter-debugBefore, steadyHeartbeats)
	}
	if got := inspector.callCount() - runtimeBefore; got != steadyHeartbeats {
		t.Fatalf("steady-state heartbeats skipped Docker runtime health: calls=%d want %d", got, steadyHeartbeats)
	}

	// Direct strict Health never queries /metrics even when a baseline exists.
	if err := managed.Health(ctx); err != nil {
		t.Fatal(err)
	}
	getsStrict, _, metricsStrict := fixture.counts()
	if getsStrict != getsAfter+1 || metricsStrict != metricsAfter {
		t.Fatalf("strict Health touched /metrics or skipped GET /config/: gets=%d->%d metrics=%d->%d", getsAfter, getsStrict, metricsAfter, metricsStrict)
	}
}

func TestCaddyHeartbeatDetectsExternalConfigWipeMutationAndFailedReloadImmediately(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	observed := newHeartbeatObservationFixture(t, store)

	fixture, caddy := newCaddyAdminUnixFixture(t)
	inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
	caddy.SystemGateway = inspector
	managed := &ManagedGatewayDriver{Caddy: caddy, Layer4: &fakeLayer4Provisioner{}, Runtime: &fakeGatewayRuntimeProvisioner{}}
	if err := applyGatewayDesiredState(ctx, store, managed, gatewayState(1, 3000), nil); err != nil {
		t.Fatal(err)
	}
	agentClient := Client{GatewayDriver: managed}
	if err := agentClient.Heartbeat(ctx, store); err != nil {
		t.Fatal(err)
	}
	if healthy := <-observed; !healthy {
		t.Fatal("initial steady-state heartbeat reported unhealthy")
	}

	// 1) External POST /load wiping config to Admin-only bootstrap is detected
	// on the very next heartbeat, and write INFO log is preserved.
	_, _, writesBefore, _ := fixture.logCounts()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, caddy.AdminURL+"/load", strings.NewReader(`{"admin":{"config":{"persist":false}}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := caddy.client().Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("external /load wipe failed: resp=%v err=%v", resp, err)
	}
	_ = resp.Body.Close()
	_, _, writesAfter, _ := fixture.logCounts()
	if writesAfter <= writesBefore {
		t.Fatal("external POST /load write INFO log was not preserved")
	}
	if err := agentClient.Heartbeat(ctx, store); err != nil {
		t.Fatal(err)
	}
	if healthy := <-observed; healthy {
		t.Fatal("external bootstrap wipe was reported healthy on next heartbeat")
	}

	// 2) Restore rev 2, warm the routine baseline, then test same-timestamp
	// external DELETE /config/... (caught via non-GET Admin request counter
	// even when reload timestamp collides).
	if err := applyGatewayDesiredState(ctx, store, managed, gatewayState(2, 3100), nil); err != nil {
		t.Fatal(err)
	}
	if err := managed.HeartbeatHealth(ctx); err != nil {
		t.Fatal(err)
	}
	fixture.setPinReloadTimestamp(true)
	delReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, caddy.AdminURL+"/config/apps/http/servers", nil)
	if err != nil {
		t.Fatal(err)
	}
	delResp, err := caddy.client().Do(delReq)
	if err != nil || delResp.StatusCode != http.StatusOK {
		t.Fatalf("external DELETE /config/ failed: resp=%v err=%v", delResp, err)
	}
	_ = delResp.Body.Close()
	fixture.setPinReloadTimestamp(false)
	if err := agentClient.Heartbeat(ctx, store); err != nil {
		t.Fatal(err)
	}
	if healthy := <-observed; healthy {
		t.Fatal("same-timestamp external DELETE /config/ was missed by generation check")
	}

	// 3) Restore rev 3, warm baseline, then test failed external POST /load:
	// error log is preserved, reload_successful=0 triggers ONE immediate
	// strict GET /config/ verification, and once the retained valid config is
	// confirmed under that generation, subsequent steady heartbeats return to
	// DEBUG-only /metrics with 0 INFO logs — while an actual wipe still fails.
	if err := applyGatewayDesiredState(ctx, store, managed, gatewayState(3, 3200), nil); err != nil {
		t.Fatal(err)
	}
	if err := managed.HeartbeatHealth(ctx); err != nil {
		t.Fatal(err)
	}
	getsBefore, _, _ := fixture.counts()
	_, _, _, errorsBefore := fixture.logCounts()
	badReq, err := http.NewRequestWithContext(ctx, http.MethodPost, caddy.AdminURL+"/load", bytes.NewReader([]byte(`{invalid`)))
	if err != nil {
		t.Fatal(err)
	}
	badReq.Header.Set("Content-Type", "application/json")
	badResp, err := caddy.client().Do(badReq)
	if err != nil || badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 for invalid /load, got resp=%v err=%v", badResp, err)
	}
	_ = badResp.Body.Close()
	_, _, _, errorsAfter := fixture.logCounts()
	if errorsAfter <= errorsBefore {
		t.Fatal("Admin API error log was not preserved")
	}
	// First heartbeat after failed reload runs strict GET /config/ and
	// re-baselines the retained healthy configuration with reload_successful=0.
	if err := managed.HeartbeatHealth(ctx); err != nil {
		t.Fatalf("failed reload with intact config reported unhealthy: %v", err)
	}
	getsAfterFailureCheck, _, _ := fixture.counts()
	if getsAfterFailureCheck != getsBefore+1 {
		t.Fatalf("failed reload did not trigger immediate GET /config/: gets=%d want %d", getsAfterFailureCheck, getsBefore+1)
	}
	infoBeforeQuiet, _, _, _ := fixture.logCounts()
	for range 3 {
		if err := managed.HeartbeatHealth(ctx); err != nil {
			t.Fatalf("steady heartbeat after retained config failed: %v", err)
		}
	}
	getsQuiet, _, _ := fixture.counts()
	infoAfterQuiet, _, _, _ := fixture.logCounts()
	if getsQuiet != getsAfterFailureCheck || infoAfterQuiet != infoBeforeQuiet {
		t.Fatalf("retained healthy config after failed reload did not resume DEBUG-only heartbeats: gets=%d->%d info=%d->%d", getsAfterFailureCheck, getsQuiet, infoBeforeQuiet, infoAfterQuiet)
	}
	// An actual external bootstrap wipe from the reload_successful=0 baseline
	// must still fail immediately on the next heartbeat.
	wipeReq, err := http.NewRequestWithContext(ctx, http.MethodPost, caddy.AdminURL+"/load", strings.NewReader(`{"admin":{"config":{"persist":false}}}`))
	if err != nil {
		t.Fatal(err)
	}
	wipeReq.Header.Set("Content-Type", "application/json")
	wipeResp, err := caddy.client().Do(wipeReq)
	if err != nil || wipeResp.StatusCode != http.StatusOK {
		t.Fatalf("wipe from reload_successful=0 baseline failed: resp=%v err=%v", wipeResp, err)
	}
	_ = wipeResp.Body.Close()
	if err := managed.HeartbeatHealth(ctx); err == nil || !strings.Contains(err.Error(), "Admin bootstrap") {
		t.Fatalf("bootstrap wipe from reload_successful=0 baseline was missed: %v", err)
	}
}

func TestCaddyHeartbeatRefusesStaleFingerprintAcrossConcurrentReloadAndInstanceChange(t *testing.T) {
	ctx := context.Background()

	t.Run("concurrent wipe during GET /config/ cannot report healthy from old generation", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
		caddy.SystemGateway = inspector
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		// Simulate an external wipe landing right during GET /config/ so
		// beforeGen != afterGen; HeartbeatHealth must not return healthy from
		// the stale pre-wipe readback and must immediately re-verify and fail.
		fixture.setOnConfigGet(func(f *fakeCaddyAdminFixture) {
			f.simulateExternalReload(`{"admin":{"config":{"persist":false}}}`)
		})
		if err := caddy.HeartbeatHealth(ctx); err == nil || !strings.Contains(err.Error(), "Admin bootstrap") {
			t.Fatalf("HeartbeatHealth reported ready from stale pre-wipe GET /config/: %v", err)
		}
		caddy.mu.RLock()
		cachedAfterRace := caddy.configProbe.valid
		caddy.mu.RUnlock()
		if cachedAfterRace {
			t.Fatal("driver cached probe despite concurrent wipe during GET /config/")
		}
		gets, _, _ := fixture.counts()
		if gets != 2 {
			t.Fatalf("concurrent wipe did not immediately re-verify once: gets=%d want 2", gets)
		}
	})

	t.Run("continuous concurrent churn returns unverified instead of stale healthy", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
		caddy.SystemGateway = inspector
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		loadedConfig, err := caddyConfiguration(gatewayState(1, 3000), nil, caddy.AdminListen)
		if err != nil {
			t.Fatal(err)
		}
		var rearm func(*fakeCaddyAdminFixture)
		rearm = func(f *fakeCaddyAdminFixture) {
			f.simulateExternalReload(string(loadedConfig))
			f.setOnConfigGet(rearm)
		}
		fixture.setOnConfigGet(rearm)
		if err := caddy.HeartbeatHealth(ctx); err == nil || !strings.Contains(err.Error(), "changed during health verification") {
			t.Fatalf("continuous concurrent generation churn reported healthy: %v", err)
		}
		fixture.setOnConfigGet(nil)
		gets, _, _ := fixture.counts()
		if gets != 2 {
			t.Fatalf("bounded retry exceeded 2 attempts: gets=%d want 2", gets)
		}
		// Once churn stops, the next heartbeat verifies cleanly.
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatalf("heartbeat did not recover after churn stopped: %v", err)
		}
	})

	t.Run("socket replacement during GET /config/ re-verifies instead of caching stale inode", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
		caddy.SystemGateway = inspector
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		oldSocketID := adminSocketIdentity(fixture.socketPath)
		fixture.setOnConfigGet(func(f *fakeCaddyAdminFixture) {
			f.replaceUnixSocketGraceful(t)
		})
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatalf("socket replacement during readback did not settle on retry: %v", err)
		}
		gets, _, _ := fixture.counts()
		if gets != 2 {
			t.Fatalf("socket swap mid-read did not trigger immediate retry: gets=%d want 2", gets)
		}
		newSocketID := adminSocketIdentity(fixture.socketPath)
		if newSocketID == "" || newSocketID == oldSocketID {
			t.Fatalf("replacement socket did not allocate new inode: old=%q new=%q", oldSocketID, newSocketID)
		}
		caddy.mu.RLock()
		cachedProbe := caddy.configProbe
		caddy.mu.RUnlock()
		if !cachedProbe.valid || cachedProbe.instanceID != inspector.identity+"|"+newSocketID {
			t.Fatalf("cached probe not bound to replacement socket: %+v want instance %q", cachedProbe, inspector.identity+"|"+newSocketID)
		}
		// Subsequent steady heartbeat reuses the baseline bound to the new inode.
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatalf("steady heartbeat on replacement socket failed: %v", err)
		}
		getsAfter, _, _ := fixture.counts()
		if getsAfter != 2 {
			t.Fatalf("steady heartbeat after replacement socket did not reuse new baseline: gets=%d want 2", getsAfter)
		}
	})

	t.Run("socket transport EOF during GET /config/ fails safely and recovers on next heartbeat", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
		caddy.SystemGateway = inspector
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		oldSocketID := adminSocketIdentity(fixture.socketPath)
		fixture.setOnConfigGet(func(f *fakeCaddyAdminFixture) {
			f.replaceUnixSocket(t)
		})
		if err := caddy.HeartbeatHealth(ctx); err == nil {
			t.Fatal("aborted GET /config/ transport EOF was reported healthy")
		}
		caddy.mu.RLock()
		cachedAfterEOF := caddy.configProbe.valid
		caddy.mu.RUnlock()
		if cachedAfterEOF {
			t.Fatal("aborted GET /config/ left a valid cached probe")
		}
		// Next routine heartbeat recovers cleanly against the new listener.
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatalf("heartbeat did not recover after socket replacement EOF: %v", err)
		}
		gets, _, _ := fixture.counts()
		if gets != 2 {
			t.Fatalf("recovery after socket replacement EOF did not re-read GET /config/: gets=%d want 2", gets)
		}
		newSocketID := adminSocketIdentity(fixture.socketPath)
		if newSocketID == "" || newSocketID == oldSocketID {
			t.Fatalf("replacement socket did not allocate new inode: old=%q new=%q", oldSocketID, newSocketID)
		}
		caddy.mu.RLock()
		cachedAfterRecovery := caddy.configProbe
		caddy.mu.RUnlock()
		if !cachedAfterRecovery.valid || cachedAfterRecovery.instanceID != inspector.identity+"|"+newSocketID {
			t.Fatalf("recovered probe not bound to replacement socket: %+v want instance %q", cachedAfterRecovery, inspector.identity+"|"+newSocketID)
		}
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatalf("steady heartbeat after EOF recovery failed: %v", err)
		}
		getsAfter, _, _ := fixture.counts()
		if getsAfter != 2 {
			t.Fatalf("steady heartbeat after EOF recovery did not reuse new baseline: gets=%d want 2", getsAfter)
		}
	})

	t.Run("old generation fingerprint cannot be reused across replaced instance", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
		caddy.SystemGateway = inspector
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		// Seed routine baseline first.
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatal(err)
		}
		getsBefore, _, _ := fixture.counts()
		// Even if a replacement container somehow emitted the exact same
		// /metrics payload, the changed Docker instance identity must force a
		// strict GET /config/.
		inspector.set("docker:container-b:101:2026-11-28T00:00:00Z", nil)
		fixture.setConfig(`{"admin":{"config":{"persist":false}}}`)
		if err := caddy.HeartbeatHealth(ctx); err == nil || !strings.Contains(err.Error(), "Admin bootstrap") {
			t.Fatalf("old generation fingerprint was reused across container replacement: %v", err)
		}
		getsAfter, _, _ := fixture.counts()
		if getsAfter != getsBefore+1 {
			t.Fatalf("instance change did not force GET /config/: before=%d after=%d", getsBefore, getsAfter)
		}
	})
}

func TestCaddyHeartbeatFailsClosedOnMetricsAnomalies(t *testing.T) {
	ctx := context.Background()
	for name, configure := range map[string]func(*fakeCaddyAdminFixture){
		"http 500": func(f *fakeCaddyAdminFixture) { f.setMetricsStatus(http.StatusInternalServerError) },
		"http 404": func(f *fakeCaddyAdminFixture) { f.setMetricsStatus(http.StatusNotFound) },
		"missing reload gauge": func(f *fakeCaddyAdminFixture) {
			s := "caddy_config_last_reload_success_timestamp_seconds 1795780000\n"
			f.setMetricsOverride(&s)
		},
		"invalid reload gauge value": func(f *fakeCaddyAdminFixture) {
			s := "caddy_config_last_reload_successful 2\ncaddy_config_last_reload_success_timestamp_seconds 1795780000\n"
			f.setMetricsOverride(&s)
		},
		"zero timestamp": func(f *fakeCaddyAdminFixture) {
			s := "caddy_config_last_reload_successful 1\ncaddy_config_last_reload_success_timestamp_seconds 0\n"
			f.setMetricsOverride(&s)
		},
		"nan value": func(f *fakeCaddyAdminFixture) {
			s := "caddy_config_last_reload_successful 1\ncaddy_config_last_reload_success_timestamp_seconds NaN\n"
			f.setMetricsOverride(&s)
		},
		"malformed labels": func(f *fakeCaddyAdminFixture) {
			s := "caddy_config_last_reload_successful 1\ncaddy_config_last_reload_success_timestamp_seconds 1795780000\ncaddy_admin_http_requests_total{method=\"POST} 1\n"
			f.setMetricsOverride(&s)
		},
		"duplicate metric": func(f *fakeCaddyAdminFixture) {
			s := "caddy_config_last_reload_successful 1\ncaddy_config_last_reload_successful 1\ncaddy_config_last_reload_success_timestamp_seconds 1795780000\n"
			f.setMetricsOverride(&s)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture, caddy := newCaddyAdminUnixFixture(t)
			inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
			caddy.SystemGateway = inspector
			if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
				t.Fatal(err)
			}
			// Seed valid routine baseline first.
			if err := caddy.HeartbeatHealth(ctx); err != nil {
				t.Fatal(err)
			}
			getsBefore, _, _ := fixture.counts()
			configure(fixture)
			// Must fail closed to strict GET /config/ and refuse to cache.
			for step := 1; step <= 2; step++ {
				if err := caddy.HeartbeatHealth(ctx); err != nil {
					t.Fatalf("healthy config failed under metrics anomaly %s: %v", name, err)
				}
				getsNow, _, _ := fixture.counts()
				if getsNow != getsBefore+step {
					t.Fatalf("metrics anomaly %s reused cache at step %d: gets=%d want %d", name, step, getsNow, getsBefore+step)
				}
			}
		})
	}
}

func TestCaddyHeartbeatInvalidatesOnRestartReplacementSocketAndRuntimeError(t *testing.T) {
	t.Run("container restart", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
		caddy.SystemGateway = inspector
		ctx := context.Background()
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatal(err)
		}
		inspector.set("docker:container-a:202:2026-11-28T00:00:10Z", nil)
		fixture.setConfig(`{"admin":{"config":{"persist":false}}}`)
		if err := caddy.HeartbeatHealth(ctx); err == nil || !strings.Contains(err.Error(), "Admin bootstrap") {
			t.Fatalf("restarted container with bootstrap config was reported healthy: %v", err)
		}
		gets, _, _ := fixture.counts()
		if gets != 2 {
			t.Fatalf("container restart did not immediately re-probe GET /config/: gets=%d want 2", gets)
		}
	})

	t.Run("container replacement", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
		caddy.SystemGateway = inspector
		ctx := context.Background()
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatal(err)
		}
		inspector.set("docker:container-b:303:2026-11-28T00:00:12Z", nil)
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatal(err)
		}
		gets, _, _ := fixture.counts()
		if gets != 2 {
			t.Fatalf("container replacement did not immediately re-probe GET /config/: gets=%d want 2", gets)
		}
	})

	t.Run("runtime error invalidates cached probe", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
		caddy.SystemGateway = inspector
		ctx := context.Background()
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatal(err)
		}
		inspector.set("docker:container-a:101:2026-11-28T00:00:00Z", errors.New("agent: Caddy network attachment is incomplete"))
		if err := caddy.HeartbeatHealth(ctx); err == nil {
			t.Fatal("runtime health error was masked by cached probe")
		}
		inspector.set("docker:container-a:101:2026-11-28T00:00:00Z", nil)
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatal(err)
		}
		gets, _, _ := fixture.counts()
		if gets != 2 {
			t.Fatalf("recovery after runtime error reused stale cache: gets=%d want 2", gets)
		}
	})

	t.Run("admin unix socket replaced or removed", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
		caddy.SystemGateway = inspector
		ctx := context.Background()
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatal(err)
		}
		fixture.replaceUnixSocket(t)
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatal(err)
		}
		gets, _, _ := fixture.counts()
		if gets != 2 {
			t.Fatalf("replaced Admin socket did not trigger immediate GET /config/: gets=%d want 2", gets)
		}

		fixture.removeUnixSocket(t)
		if err := caddy.HeartbeatHealth(ctx); err == nil {
			t.Fatal("removed Admin socket was reported healthy")
		}
	})

	t.Run("stale unix socket inode with closed listener", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
		caddy.SystemGateway = inspector
		ctx := context.Background()
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatal(err)
		}
		beforeInfo, err := os.Lstat(fixture.socketPath)
		if err != nil {
			t.Fatal(err)
		}
		beforeStat, ok := beforeInfo.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatal("missing unix socket stat metadata")
		}

		// Close the listener while preserving the exact socket pathname and inode.
		fixture.closeListenerKeepSocket(t)
		afterInfo, err := os.Lstat(fixture.socketPath)
		if err != nil || afterInfo.Mode()&os.ModeSocket == 0 {
			t.Fatalf("expected stale socket file to remain in place: info=%v err=%v", afterInfo, err)
		}
		afterStat, ok := afterInfo.Sys().(*syscall.Stat_t)
		if !ok || afterStat.Dev != beforeStat.Dev || afterStat.Ino != beforeStat.Ino {
			t.Fatalf("stale socket inode changed unexpectedly: before=(%d,%d) after=(%d,%d)", beforeStat.Dev, beforeStat.Ino, afterStat.Dev, afterStat.Ino)
		}

		if err := caddy.HeartbeatHealth(ctx); err == nil {
			t.Fatal("closed Admin listener with retained socket inode was reported healthy")
		}
		gets, _, _ := fixture.counts()
		if gets != 1 {
			t.Fatalf("closed listener path issued unexpected GET /config/: gets=%d want 1", gets)
		}
		caddy.mu.RLock()
		stillValid := caddy.configProbe.valid
		caddy.mu.RUnlock()
		if stillValid {
			t.Fatal("unreachable Admin Unix socket did not invalidate cached probe")
		}

		if err := os.Remove(fixture.socketPath); err != nil {
			t.Fatal(err)
		}
		fixture.startUnix(t)
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatal(err)
		}
		gets, _, _ = fixture.counts()
		if gets != 2 {
			t.Fatalf("recovery after unreachable socket did not re-probe GET /config/: gets=%d want 2", gets)
		}
	})
}

func TestCaddyHeartbeatInvalidatesOnApplyWebsiteReloadRecoveryAndStrictPaths(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	fixture, caddy := newCaddyAdminUnixFixture(t)
	inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
	caddy.SystemGateway = inspector
	caddy.Sites = func(context.Context) ([]CaddySite, error) { return nil, nil }
	caddy.SiteRuntime = func(context.Context, gateway.DesiredState, []CaddySite) error { return nil }
	caddy.SiteContainer = true
	store.sharedCaddy = caddy

	managed := &ManagedGatewayDriver{Caddy: caddy, Layer4: &fakeLayer4Provisioner{}, Runtime: &fakeGatewayRuntimeProvisioner{}}
	if err := applyGatewayDesiredState(ctx, store, managed, gatewayState(1, 3000), nil); err != nil {
		t.Fatal(err)
	}
	gets, _, _ := fixture.counts()
	if gets != 1 {
		t.Fatalf("initial apply GET /config/ count = %d, want 1", gets)
	}

	if err := applyGatewayDesiredState(ctx, store, managed, gatewayState(2, 3100), nil); err != nil {
		t.Fatal(err)
	}
	gets, _, _ = fixture.counts()
	if gets != 2 {
		t.Fatalf("second apply did not run immediate strict GET /config/: gets=%d want 2", gets)
	}

	if err := managed.Health(ctx); err != nil {
		t.Fatal(err)
	}
	if status, err := managed.GetRouteStatus(ctx, "cpa-route"); err != nil || status != "ready" {
		t.Fatalf("GetRouteStatus = %q, %v", status, err)
	}
	if err := waitForGateway(ctx, managed); err != nil {
		t.Fatal(err)
	}
	if err := store.restoreGatewayConfiguration(ctx); err != nil {
		t.Fatal(err)
	}
	gets, _, metrics := fixture.counts()
	if gets != 6 || metrics != 0 {
		t.Fatalf("strict verification paths did not stay uncached and /metrics-free: gets=%d metrics=%d want 6 and 0", gets, metrics)
	}

	if err := managed.ReloadWebsites(ctx); err != nil {
		t.Fatal(err)
	}
	if err := managed.HeartbeatHealth(ctx); err != nil {
		t.Fatal(err)
	}
	gets, _, _ = fixture.counts()
	if gets != 7 {
		t.Fatalf("ReloadWebsites did not invalidate cached heartbeat probe: gets=%d want 7", gets)
	}

	// Steady heartbeat after ReloadWebsites re-verification uses /metrics only.
	if err := managed.HeartbeatHealth(ctx); err != nil {
		t.Fatal(err)
	}
	gets, _, _ = fixture.counts()
	if gets != 7 {
		t.Fatalf("post-reload steady heartbeat did not reuse verified generation: gets=%d want 7", gets)
	}

	fixture.setLoadStatus(http.StatusInternalServerError)
	if err := applyGatewayDesiredState(ctx, store, managed, gatewayState(3, 3200), nil); err == nil {
		t.Fatal("failed /load was accepted")
	}
	fixture.setLoadStatus(http.StatusOK)
	if err := managed.HeartbeatHealth(ctx); err != nil {
		t.Fatal(err)
	}
	gets, _, _ = fixture.counts()
	if gets != 8 {
		t.Fatalf("failed /load did not invalidate cached heartbeat probe: gets=%d want 8", gets)
	}
}

func TestCaddyHeartbeatStaysStrictForNativeAndUnidentifiedRuntimes(t *testing.T) {
	ctx := context.Background()

	t.Run("native or nil SystemGateway", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		for range 4 {
			if err := caddy.HeartbeatHealth(ctx); err != nil {
				t.Fatal(err)
			}
		}
		gets, _, metrics := fixture.counts()
		if gets != 4 || metrics != 0 {
			t.Fatalf("native/nil runtime did not stay strict: gets=%d metrics=%d want 4 and 0", gets, metrics)
		}
	})

	t.Run("health-only SystemGateway without instance identity", func(t *testing.T) {
		fixture, caddy := newCaddyAdminHTTPFixture(t)
		inspector := &fakeCaddyHealthOnlyInspector{}
		caddy.SystemGateway = inspector
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if err := caddy.HeartbeatHealth(ctx); err != nil {
				t.Fatal(err)
			}
		}
		gets, _, metrics := fixture.counts()
		if gets != 3 || metrics != 0 || inspector.callCount() != 3 {
			t.Fatalf("health-only runtime did not stay strict: gets=%d metrics=%d healthCalls=%d", gets, metrics, inspector.callCount())
		}
	})

	t.Run("empty runtime identity fails closed", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: ""}
		caddy.SystemGateway = inspector
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if err := caddy.HeartbeatHealth(ctx); err != nil {
				t.Fatal(err)
			}
		}
		gets, _, metrics := fixture.counts()
		if gets != 3 || metrics != 0 {
			t.Fatalf("unidentified runtime did not stay strict: gets=%d metrics=%d want 3 and 0", gets, metrics)
		}
	})

	t.Run("docker runtime with loopback HTTP admin endpoint stays strict", func(t *testing.T) {
		fixture, caddy := newCaddyAdminHTTPFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
		caddy.SystemGateway = inspector
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if err := caddy.HeartbeatHealth(ctx); err != nil {
				t.Fatal(err)
			}
		}
		gets, _, metrics := fixture.counts()
		if gets != 3 || metrics != 0 || inspector.callCount() != 3 {
			t.Fatalf("loopback HTTP Admin endpoint did not stay strict: gets=%d metrics=%d runtimeCalls=%d", gets, metrics, inspector.callCount())
		}
		fixture.httpServer.Close()
		if err := caddy.HeartbeatHealth(ctx); err == nil {
			t.Fatal("unreachable loopback HTTP Admin endpoint was reported healthy")
		}
	})
}

func TestCaddyHeartbeatNeverReportsBootstrapOrErrorAsReady(t *testing.T) {
	ctx := context.Background()
	fixture, caddy := newCaddyAdminUnixFixture(t)
	inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
	caddy.SystemGateway = inspector

	// With zero routes configured, bootstrap-only config is acceptable.
	if err := caddy.HeartbeatHealth(ctx); err != nil {
		t.Fatal(err)
	}
	// If expected routes later appear while Caddy still serves only bootstrap,
	// the earlier bootstrap probe must never be reused as ready even when
	// /metrics still reports reload_successful=1.
	caddy.mu.Lock()
	caddy.state = gatewayState(1, 3000)
	caddy.mu.Unlock()
	if err := caddy.HeartbeatHealth(ctx); err == nil || !strings.Contains(err.Error(), "Admin bootstrap") {
		t.Fatalf("bootstrap state was reported ready once routes were expected: %v", err)
	}

	fixture.setGetStatus(http.StatusServiceUnavailable)
	if err := caddy.HeartbeatHealth(ctx); err == nil {
		t.Fatal("HTTP 503 from Admin API was reported healthy")
	}
	fixture.setGetStatus(http.StatusOK)
	fixture.setConfig(`not-json`)
	if err := caddy.HeartbeatHealth(ctx); err == nil {
		t.Fatal("invalid JSON from Admin API was reported healthy")
	}
}

func TestParseCaddyAdminMetricsGeneration(t *testing.T) {
	base := `# HELP caddy_config_last_reload_successful test
# TYPE caddy_config_last_reload_successful gauge
caddy_config_last_reload_successful 1
caddy_config_last_reload_success_timestamp_seconds 1.79578e+09
process_start_time_seconds 1795779900
caddy_admin_http_requests_total{code="200",handler="admin",method="GET",path="/metrics"} 3
caddy_admin_http_requests_total{code="200",handler="admin",method="GET",path="/config/"} 1
caddy_admin_http_requests_total{code="200",handler="admin",method="POST",path="/load"} 1
`
	gen1, ok := parseCaddyAdminMetricsGeneration([]byte(base))
	if !ok || gen1 == "" {
		t.Fatal("valid metrics payload was rejected")
	}

	// Read-only Admin methods (GET, HEAD, OPTIONS) on any built-in Admin path
	// (including /debug/pprof and /debug/vars), equivalent float formatting,
	// and optional Prometheus timestamps must preserve generation.
	sameGen := `caddy_config_last_reload_successful 1 1795780001000
caddy_config_last_reload_success_timestamp_seconds 1795780000
process_start_time_seconds 1.7957799e+09
caddy_admin_http_requests_total{code="200",handler="admin",method="GET",path="/metrics"} 99
caddy_admin_http_requests_total{code="200",handler="admin",method="GET",path="/config/"} 7
caddy_admin_http_requests_total{code="200",handler="admin",method="GET",path="/debug/pprof/"} 14
caddy_admin_http_requests_total{code="200",handler="admin",method="GET",path="/debug/vars"} 5
caddy_admin_http_requests_total{code="200",handler="admin",method="HEAD",path="/config/"} 2
caddy_admin_http_requests_total{code="200",handler="admin",method="OPTIONS",path="/load"} 3
caddy_admin_http_request_errors_total{handler="admin",method="GET",path="/debug/pprof/"} 1
caddy_admin_http_requests_total{code="200",handler="admin",method="POST",path="/load"} 1
`
	gen2, ok := parseCaddyAdminMetricsGeneration([]byte(sameGen))
	if !ok || gen2 != gen1 {
		t.Fatalf("read-only Admin requests changed generation: %q != %q (ok=%v)", gen2, gen1, ok)
	}

	// reload_successful=0 is valid and produces a distinct generation token so
	// a single failed reload forces immediate verification and then lets a
	// retained healthy config resume DEBUG-only heartbeats.
	genFailedReload, ok := parseCaddyAdminMetricsGeneration([]byte(strings.Replace(base, "successful 1", "successful 0", 1)))
	if !ok || genFailedReload == "" || genFailedReload == gen1 {
		t.Fatalf("reload_successful=0 did not produce a distinct valid generation: ok=%v gen=%q", ok, genFailedReload)
	}

	// Any mutating Admin request, mutating error counter, reload timestamp
	// change, or process start change must produce a different generation.
	for name, payload := range map[string]string{
		"reload timestamp changed": strings.Replace(base, "1.79578e+09", "1795780001", 1),
		"post load incremented":    strings.Replace(base, `path="/load"} 1`, `path="/load"} 2`, 1),
		"delete config added":      base + "caddy_admin_http_requests_total{code=\"200\",handler=\"admin\",method=\"DELETE\",path=\"/config/\"} 1\n",
		"admin error added":        base + "caddy_admin_http_request_errors_total{handler=\"admin\",method=\"POST\",path=\"/load\"} 1\n",
		"process start changed":    strings.Replace(base, "1795779900", "1795779999", 1),
	} {
		gen, ok := parseCaddyAdminMetricsGeneration([]byte(payload))
		if !ok || gen == gen1 {
			t.Fatalf("%s did not change generation: ok=%v gen=%q", name, ok, gen)
		}
	}

	// Invalid or incomplete payloads must fail closed.
	for name, bad := range map[string]string{
		"empty":                  "",
		"reload out of range":    strings.Replace(base, "successful 1", "successful 2", 1),
		"missing timestamp":      "caddy_config_last_reload_successful 1\n",
		"negative counter":       base + "caddy_admin_http_requests_total{code=\"200\",handler=\"admin\",method=\"POST\",path=\"/config/\"} -1\n",
		"duplicate label":        "caddy_config_last_reload_successful 1\ncaddy_config_last_reload_success_timestamp_seconds 1795780000\ncaddy_admin_http_requests_total{method=\"POST\",method=\"POST\"} 1\n",
		"bad escape in label":    "caddy_config_last_reload_successful 1\ncaddy_config_last_reload_success_timestamp_seconds 1795780000\ncaddy_admin_http_requests_total{path=\"\\x\"} 1\n",
		"unclosed quote":         "caddy_config_last_reload_successful 1\ncaddy_config_last_reload_success_timestamp_seconds 1795780000\ncaddy_admin_http_requests_total{path=\"/load} 1\n",
		"labeled reload gauge":   "caddy_config_last_reload_successful{a=\"b\"} 1\ncaddy_config_last_reload_success_timestamp_seconds 1795780000\n",
		"non-integer timestamp":  "caddy_config_last_reload_successful 1 badts\ncaddy_config_last_reload_success_timestamp_seconds 1795780000\n",
		"infinite reload time":   "caddy_config_last_reload_successful 1\ncaddy_config_last_reload_success_timestamp_seconds +Inf\n",
		"duplicate admin series": base + "caddy_admin_http_requests_total{code=\"200\",handler=\"admin\",method=\"POST\",path=\"/load\"} 1\n",
	} {
		if gen, ok := parseCaddyAdminMetricsGeneration([]byte(bad)); ok {
			t.Fatalf("%s unexpectedly accepted with generation %q", name, gen)
		}
	}
}

func TestDockerContainerRuntimeIdentity(t *testing.T) {
	base := client.ContainerInspectResult{Container: container.InspectResponse{
		ID:    "abcdef0123456789",
		State: &container.State{Running: true, Pid: 4242, StartedAt: "2026-11-28T00:00:00.123456789Z"},
	}}
	id := dockerContainerRuntimeIdentity(base)
	if id == "" {
		t.Fatal("running container with valid ID/PID/StartedAt returned empty identity")
	}

	restarted := base
	restarted.Container.State = &container.State{Running: true, Pid: 5252, StartedAt: "2026-11-28T00:05:00.000000000Z"}
	if dockerContainerRuntimeIdentity(restarted) == id {
		t.Fatal("restarted container produced the same runtime identity")
	}

	replaced := base
	replaced.Container.ID = "9876543210fedcba"
	if dockerContainerRuntimeIdentity(replaced) == id {
		t.Fatal("replaced container produced the same runtime identity")
	}

	for name, invalid := range map[string]client.ContainerInspectResult{
		"empty id":       {Container: container.InspectResponse{State: base.Container.State}},
		"nil state":      {Container: container.InspectResponse{ID: base.Container.ID}},
		"not running":    {Container: container.InspectResponse{ID: base.Container.ID, State: &container.State{Running: false, Pid: 4242, StartedAt: base.Container.State.StartedAt}}},
		"paused":         {Container: container.InspectResponse{ID: base.Container.ID, State: &container.State{Running: true, Paused: true, Pid: 4242, StartedAt: base.Container.State.StartedAt}}},
		"restarting":     {Container: container.InspectResponse{ID: base.Container.ID, State: &container.State{Running: true, Restarting: true, Pid: 4242, StartedAt: base.Container.State.StartedAt}}},
		"dead":           {Container: container.InspectResponse{ID: base.Container.ID, State: &container.State{Running: true, Dead: true, Pid: 4242, StartedAt: base.Container.State.StartedAt}}},
		"zero pid":       {Container: container.InspectResponse{ID: base.Container.ID, State: &container.State{Running: true, Pid: 0, StartedAt: base.Container.State.StartedAt}}},
		"empty started":  {Container: container.InspectResponse{ID: base.Container.ID, State: &container.State{Running: true, Pid: 4242, StartedAt: ""}}},
		"zero timestamp": {Container: container.InspectResponse{ID: base.Container.ID, State: &container.State{Running: true, Pid: 4242, StartedAt: "0001-01-01T00:00:00Z"}}},
	} {
		if got := dockerContainerRuntimeIdentity(invalid); got != "" {
			t.Fatalf("%s unexpectedly produced runtime identity %q", name, got)
		}
	}
}
