package agent

import (
	"context"
	"encoding/json"
	"errors"
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
	"time"

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

type fakeCaddyAdminFixture struct {
	mu         sync.Mutex
	config     []byte
	getStatus  int
	loadStatus int
	getCalls   int
	loadCalls  int
	socketPath string
	listener   net.Listener
	server     *http.Server
	httpServer *httptest.Server
}

func newCaddyAdminUnixFixture(t *testing.T) (*fakeCaddyAdminFixture, *CaddyGatewayDriver) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "vastora-caddy-hb-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "admin.sock")
	fixture := &fakeCaddyAdminFixture{
		config:     []byte(`{"admin":{"config":{"persist":false}}}`),
		getStatus:  http.StatusOK,
		loadStatus: http.StatusOK,
		socketPath: socketPath,
	}
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
	fixture := &fakeCaddyAdminFixture{
		config:     []byte(`{"admin":{"config":{"persist":false}}}`),
		getStatus:  http.StatusOK,
		loadStatus: http.StatusOK,
	}
	fixture.httpServer = httptest.NewServer(http.HandlerFunc(fixture.handle))
	t.Cleanup(fixture.httpServer.Close)
	driver, err := NewCaddyGatewayDriver(fixture.httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	return fixture, driver
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
	defer fixture.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/config/":
		fixture.getCalls++
		w.WriteHeader(fixture.getStatus)
		_, _ = w.Write(fixture.config)
	case r.Method == http.MethodPost && r.URL.Path == "/load":
		fixture.loadCalls++
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(fixture.loadStatus)
		if fixture.loadStatus >= 200 && fixture.loadStatus < 300 {
			fixture.config = body
		}
	default:
		http.NotFound(w, r)
	}
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

func (fixture *fakeCaddyAdminFixture) counts() (gets, loads int) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.getCalls, fixture.loadCalls
}

func newHeartbeatObservationFixture(t *testing.T, store *Store) chan bool {
	t.Helper()
	observed := make(chan bool, 16)
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

func TestCaddyHeartbeatBoundsConfigReadsWithin60sWindow(t *testing.T) {
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
	currentTime := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	caddy.now = func() time.Time { return currentTime }

	managed := &ManagedGatewayDriver{Caddy: caddy, Layer4: &fakeLayer4Provisioner{}, Runtime: &fakeGatewayRuntimeProvisioner{}}
	if err := applyGatewayDesiredState(ctx, store, managed, gatewayState(1, 3000), nil); err != nil {
		t.Fatal(err)
	}
	gets, loads := fixture.counts()
	if gets != 1 || loads != 1 {
		t.Fatalf("post-apply verification counts = gets:%d loads:%d, want 1 and 1", gets, loads)
	}

	agentClient := Client{GatewayDriver: managed}
	// StartupHeartbeat always performs an immediate uncached GET /config/.
	if err := agentClient.StartupHeartbeat(ctx, store); err != nil {
		t.Fatal(err)
	}
	if healthy := <-observed; !healthy {
		t.Fatal("startup heartbeat reported unhealthy gateway")
	}
	gets, _ = fixture.counts()
	if gets != 2 {
		t.Fatalf("StartupHeartbeat did not perform immediate GET /config/: gets=%d want 2", gets)
	}
	runtimeBefore := inspector.callCount()

	// Routine heartbeats within the 60s window still verify runtime liveness
	// every 15s while avoiding repetitive GET /config/ reads.
	for _, advance := range []time.Duration{0, 15 * time.Second, 15 * time.Second, 15 * time.Second} {
		currentTime = currentTime.Add(advance)
		if err := agentClient.Heartbeat(ctx, store); err != nil {
			t.Fatal(err)
		}
		if healthy := <-observed; !healthy {
			t.Fatalf("routine heartbeat at %v reported unhealthy gateway", currentTime)
		}
	}
	gets, _ = fixture.counts()
	if gets != 2 {
		t.Fatalf("routine heartbeats within 60s window ran extra GET /config/: gets=%d want 2", gets)
	}
	if got := inspector.callCount() - runtimeBefore; got != 4 {
		t.Fatalf("routine heartbeats skipped runtime liveness checks: calls=%d want 4", got)
	}

	// At the 60s boundary, routine heartbeat re-verifies GET /config/ and
	// opens a new 60s window (1 GET per 4x 15s heartbeats = 75% reduction).
	currentTime = currentTime.Add(15 * time.Second)
	if err := agentClient.Heartbeat(ctx, store); err != nil {
		t.Fatal(err)
	}
	if healthy := <-observed; !healthy {
		t.Fatal("bounded re-verification heartbeat reported unhealthy gateway")
	}
	gets, _ = fixture.counts()
	if gets != 3 {
		t.Fatalf("routine heartbeat at 60s boundary did not re-verify GET /config/: gets=%d want 3", gets)
	}
	for range 3 {
		currentTime = currentTime.Add(15 * time.Second)
		if err := agentClient.Heartbeat(ctx, store); err != nil {
			t.Fatal(err)
		}
		if healthy := <-observed; !healthy {
			t.Fatal("routine heartbeat in second window reported unhealthy gateway")
		}
	}
	gets, _ = fixture.counts()
	if gets != 3 {
		t.Fatalf("second 60s window ran unexpected GET /config/: gets=%d want 3", gets)
	}
	currentTime = currentTime.Add(15 * time.Second)
	if err := agentClient.Heartbeat(ctx, store); err != nil {
		t.Fatal(err)
	}
	if healthy := <-observed; !healthy {
		t.Fatal("120s re-verification heartbeat reported unhealthy gateway")
	}
	gets, _ = fixture.counts()
	if gets != 4 {
		t.Fatalf("routine heartbeat at 120s boundary did not re-verify GET /config/: gets=%d want 4", gets)
	}
}

func TestCaddyHeartbeatDetectsSilentConfigLossAtBoundedInterval(t *testing.T) {
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
	currentTime := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	caddy.now = func() time.Time { return currentTime }

	managed := &ManagedGatewayDriver{Caddy: caddy, Layer4: &fakeLayer4Provisioner{}, Runtime: &fakeGatewayRuntimeProvisioner{}}
	if err := applyGatewayDesiredState(ctx, store, managed, gatewayState(1, 3000), nil); err != nil {
		t.Fatal(err)
	}

	// Simulate an external Admin-socket reset to bootstrap-only config without
	// stopping, restarting, or replacing the container.
	fixture.setConfig(`{"admin":{"config":{"persist":false}}}`)

	agentClient := Client{GatewayDriver: managed}
	currentTime = currentTime.Add(45 * time.Second)
	if err := agentClient.Heartbeat(ctx, store); err != nil {
		t.Fatal(err)
	}
	if healthy := <-observed; !healthy {
		t.Fatal("unexpected re-probe before bounded window elapsed")
	}

	// Reaching the 60s bound detects the missing gateway configuration and
	// invalidates the cached probe immediately.
	currentTime = currentTime.Add(15 * time.Second)
	if err := agentClient.Heartbeat(ctx, store); err != nil {
		t.Fatal(err)
	}
	if healthy := <-observed; healthy {
		t.Fatal("bootstrap-only Caddy was reported healthy past the 60s re-verification bound")
	}
	if err := managed.HeartbeatHealth(ctx); err == nil || !strings.Contains(err.Error(), "Admin bootstrap") {
		t.Fatalf("expected bootstrap error on subsequent heartbeat after invalidation, got %v", err)
	}
}

func TestCaddyHeartbeatInvalidatesOnRestartReplacementSocketAndRuntimeError(t *testing.T) {
	t.Run("container restart", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
		caddy.SystemGateway = inspector
		currentTime := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
		caddy.now = func() time.Time { return currentTime }

		ctx := context.Background()
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatal(err)
		}
		// Container restarted in place: same container ID, new PID and start
		// time, and Caddy came back with only its bootstrap config.
		inspector.set("docker:container-a:202:2026-11-28T00:00:10Z", nil)
		fixture.setConfig(`{"admin":{"config":{"persist":false}}}`)
		currentTime = currentTime.Add(15 * time.Second)
		if err := caddy.HeartbeatHealth(ctx); err == nil || !strings.Contains(err.Error(), "Admin bootstrap") {
			t.Fatalf("restarted container with bootstrap config was reported healthy: %v", err)
		}
		gets, _ := fixture.counts()
		if gets != 2 {
			t.Fatalf("container restart did not immediately re-probe GET /config/: gets=%d want 2", gets)
		}
	})

	t.Run("container replacement", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
		caddy.SystemGateway = inspector
		currentTime := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
		caddy.now = func() time.Time { return currentTime }

		ctx := context.Background()
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatal(err)
		}
		inspector.set("docker:container-b:303:2026-11-28T00:00:12Z", nil)
		currentTime = currentTime.Add(15 * time.Second)
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatal(err)
		}
		gets, _ := fixture.counts()
		if gets != 2 {
			t.Fatalf("container replacement did not immediately re-probe GET /config/: gets=%d want 2", gets)
		}
	})

	t.Run("runtime error invalidates cached probe", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
		caddy.SystemGateway = inspector
		currentTime := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
		caddy.now = func() time.Time { return currentTime }

		ctx := context.Background()
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatal(err)
		}
		inspector.set("docker:container-a:101:2026-11-28T00:00:00Z", errors.New("agent: Caddy network attachment is incomplete"))
		currentTime = currentTime.Add(15 * time.Second)
		if err := caddy.HeartbeatHealth(ctx); err == nil {
			t.Fatal("runtime health error was masked by cached probe")
		}
		// Recovery with the same container identity must still re-probe GET /config/.
		inspector.set("docker:container-a:101:2026-11-28T00:00:00Z", nil)
		currentTime = currentTime.Add(15 * time.Second)
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatal(err)
		}
		gets, _ := fixture.counts()
		if gets != 2 {
			t.Fatalf("recovery after runtime error reused stale cache: gets=%d want 2", gets)
		}
	})

	t.Run("admin unix socket replaced or removed", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
		caddy.SystemGateway = inspector
		currentTime := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
		caddy.now = func() time.Time { return currentTime }

		ctx := context.Background()
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatal(err)
		}
		fixture.replaceUnixSocket(t)
		currentTime = currentTime.Add(15 * time.Second)
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatal(err)
		}
		gets, _ := fixture.counts()
		if gets != 2 {
			t.Fatalf("replaced Admin socket did not trigger immediate GET /config/: gets=%d want 2", gets)
		}

		fixture.removeUnixSocket(t)
		currentTime = currentTime.Add(15 * time.Second)
		if err := caddy.HeartbeatHealth(ctx); err == nil {
			t.Fatal("removed Admin socket was reported healthy")
		}
	})

	t.Run("stale unix socket inode with closed listener", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
		caddy.SystemGateway = inspector
		currentTime := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
		caddy.now = func() time.Time { return currentTime }

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

		currentTime = currentTime.Add(15 * time.Second)
		if err := caddy.HeartbeatHealth(ctx); err == nil {
			t.Fatal("closed Admin listener with retained socket inode was reported healthy")
		}
		gets, _ := fixture.counts()
		if gets != 1 {
			t.Fatalf("closed listener path issued unexpected GET /config/: gets=%d want 1", gets)
		}
		caddy.mu.RLock()
		stillValid := caddy.configProbe.valid
		caddy.mu.RUnlock()
		if stillValid {
			t.Fatal("unreachable Admin Unix socket did not invalidate cached probe")
		}

		// Once the listener recovers, the next routine heartbeat must perform a
		// fresh GET /config/.
		if err := os.Remove(fixture.socketPath); err != nil {
			t.Fatal(err)
		}
		fixture.startUnix(t)
		currentTime = currentTime.Add(15 * time.Second)
		if err := caddy.HeartbeatHealth(ctx); err != nil {
			t.Fatal(err)
		}
		gets, _ = fixture.counts()
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
	currentTime := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	caddy.now = func() time.Time { return currentTime }
	store.sharedCaddy = caddy

	managed := &ManagedGatewayDriver{Caddy: caddy, Layer4: &fakeLayer4Provisioner{}, Runtime: &fakeGatewayRuntimeProvisioner{}}
	if err := applyGatewayDesiredState(ctx, store, managed, gatewayState(1, 3000), nil); err != nil {
		t.Fatal(err)
	}
	gets, _ := fixture.counts()
	if gets != 1 {
		t.Fatalf("initial apply GET /config/ count = %d, want 1", gets)
	}

	// Applying a new revision always runs an immediate strict GET /config/.
	currentTime = currentTime.Add(10 * time.Second)
	if err := applyGatewayDesiredState(ctx, store, managed, gatewayState(2, 3100), nil); err != nil {
		t.Fatal(err)
	}
	gets, _ = fixture.counts()
	if gets != 2 {
		t.Fatalf("second apply did not run immediate strict GET /config/: gets=%d want 2", gets)
	}

	// Direct strict Health, GetRouteStatus, waitForGateway, and startup
	// recovery always bypass the routine cache.
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
	gets, _ = fixture.counts()
	if gets != 6 {
		t.Fatalf("strict verification paths did not run uncached GET /config/: gets=%d want 6", gets)
	}

	// Website reload invalidates the cached probe so the next routine
	// heartbeat immediately re-verifies GET /config/.
	if err := managed.ReloadWebsites(ctx); err != nil {
		t.Fatal(err)
	}
	currentTime = currentTime.Add(15 * time.Second)
	if err := managed.HeartbeatHealth(ctx); err != nil {
		t.Fatal(err)
	}
	gets, _ = fixture.counts()
	if gets != 7 {
		t.Fatalf("ReloadWebsites did not invalidate cached heartbeat probe: gets=%d want 7", gets)
	}

	// A failed /load attempt also invalidates the cached probe.
	fixture.setLoadStatus(http.StatusInternalServerError)
	if err := applyGatewayDesiredState(ctx, store, managed, gatewayState(3, 3200), nil); err == nil {
		t.Fatal("failed /load was accepted")
	}
	fixture.setLoadStatus(http.StatusOK)
	currentTime = currentTime.Add(15 * time.Second)
	if err := managed.HeartbeatHealth(ctx); err != nil {
		t.Fatal(err)
	}
	gets, _ = fixture.counts()
	if gets != 8 {
		t.Fatalf("failed /load did not invalidate cached heartbeat probe: gets=%d want 8", gets)
	}

	// Backwards clock movement fails closed and forces a live GET /config/.
	currentTime = currentTime.Add(-5 * time.Second)
	if err := managed.HeartbeatHealth(ctx); err != nil {
		t.Fatal(err)
	}
	gets, _ = fixture.counts()
	if gets != 9 {
		t.Fatalf("backwards clock jump did not fail closed to GET /config/: gets=%d want 9", gets)
	}
}

func TestCaddyHeartbeatStaysStrictForNativeAndUnidentifiedRuntimes(t *testing.T) {
	ctx := context.Background()

	t.Run("native or nil SystemGateway", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		currentTime := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
		caddy.now = func() time.Time { return currentTime }
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		for range 4 {
			if err := caddy.HeartbeatHealth(ctx); err != nil {
				t.Fatal(err)
			}
			currentTime = currentTime.Add(15 * time.Second)
		}
		gets, _ := fixture.counts()
		if gets != 4 {
			t.Fatalf("native/nil runtime skipped GET /config/: gets=%d want 4", gets)
		}
	})

	t.Run("health-only SystemGateway without instance identity", func(t *testing.T) {
		fixture, caddy := newCaddyAdminHTTPFixture(t)
		inspector := &fakeCaddyHealthOnlyInspector{}
		caddy.SystemGateway = inspector
		currentTime := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
		caddy.now = func() time.Time { return currentTime }
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if err := caddy.HeartbeatHealth(ctx); err != nil {
				t.Fatal(err)
			}
			currentTime = currentTime.Add(15 * time.Second)
		}
		gets, _ := fixture.counts()
		if gets != 3 || inspector.callCount() != 3 {
			t.Fatalf("health-only runtime skipped GET /config/: gets=%d healthCalls=%d want 3 and 3", gets, inspector.callCount())
		}
	})

	t.Run("empty runtime identity fails closed", func(t *testing.T) {
		fixture, caddy := newCaddyAdminUnixFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: ""}
		caddy.SystemGateway = inspector
		currentTime := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
		caddy.now = func() time.Time { return currentTime }
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if err := caddy.HeartbeatHealth(ctx); err != nil {
				t.Fatal(err)
			}
			currentTime = currentTime.Add(15 * time.Second)
		}
		gets, _ := fixture.counts()
		if gets != 3 {
			t.Fatalf("unidentified runtime skipped GET /config/: gets=%d want 3", gets)
		}
	})

	t.Run("docker runtime with loopback HTTP admin endpoint stays strict", func(t *testing.T) {
		fixture, caddy := newCaddyAdminHTTPFixture(t)
		inspector := &fakeCaddyRuntimeInspector{identity: "docker:container-a:101:2026-11-28T00:00:00Z"}
		caddy.SystemGateway = inspector
		currentTime := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
		caddy.now = func() time.Time { return currentTime }
		if err := caddy.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if err := caddy.HeartbeatHealth(ctx); err != nil {
				t.Fatal(err)
			}
			currentTime = currentTime.Add(15 * time.Second)
		}
		gets, _ := fixture.counts()
		if gets != 3 || inspector.callCount() != 3 {
			t.Fatalf("loopback HTTP Admin endpoint cached GET /config/: gets=%d runtimeCalls=%d want 3 and 3", gets, inspector.callCount())
		}
		// If the loopback Admin listener disappears while the inspected Docker
		// container still appears healthy, the next heartbeat fails immediately.
		fixture.httpServer.Close()
		currentTime = currentTime.Add(15 * time.Second)
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
	currentTime := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	caddy.now = func() time.Time { return currentTime }

	// With zero routes configured, bootstrap-only config is acceptable.
	if err := caddy.HeartbeatHealth(ctx); err != nil {
		t.Fatal(err)
	}
	// If expected routes later appear while Caddy still serves only bootstrap,
	// the earlier bootstrap probe must never be reused as ready.
	caddy.mu.Lock()
	caddy.state = gatewayState(1, 3000)
	caddy.mu.Unlock()
	currentTime = currentTime.Add(15 * time.Second)
	if err := caddy.HeartbeatHealth(ctx); err == nil || !strings.Contains(err.Error(), "Admin bootstrap") {
		t.Fatalf("bootstrap state was reported ready once routes were expected: %v", err)
	}

	// HTTP failure and malformed JSON must fail immediately and never cache.
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
