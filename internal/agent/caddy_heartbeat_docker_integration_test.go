//go:build integration

package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/gateway"
)

type realCaddyAdminLogCounts struct {
	infoReceived   int
	infoGetConfig  int
	infoPostLoad   int
	infoGetMetrics int
	adminErrors    int
}

// TestCaddyDockerHeartbeatMetricsSilenceRealDocker exercises the pinned
// official Caddy 2.11.4 Docker image inside the isolated GitHub-hosted
// integration runner to verify:
//  1. steady-state routine heartbeats emit 0 admin.api INFO "received request"
//     lines while executing real /metrics checks over the Unix Admin socket,
//  2. Admin write (POST /load) and error logs remain intact,
//  3. failed reloads, external config wipes, and container restarts invalidate
//     the cached probe and trigger immediate strict GET /config/ verification.
func TestCaddyDockerHeartbeatMetricsSilenceRealDocker(t *testing.T) {
	packageHostedRunner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	docker, err := client.New(client.WithHost("unix:///var/run/docker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()

	_, netBeforeErr := docker.NetworkInspect(ctx, dockerruntime.NetworkName, client.NetworkInspectOptions{})
	if err := dockerruntime.EnsureNetwork(ctx, docker); err != nil {
		t.Fatal(err)
	}
	createdNetworkID := ""
	if netBeforeErr != nil {
		inspected, err := docker.NetworkInspect(ctx, dockerruntime.NetworkName, client.NetworkInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		createdNetworkID = inspected.Network.ID
	}

	root, err := os.MkdirTemp("/tmp", "vastora-caddy-ci-")
	if err != nil {
		t.Fatal(err)
	}
	suffix := packageIdentity(t.Name() + filepath.Base(root))
	name := "vastora-caddy-hb-ci-" + suffix
	dataVolume := name + "-data"
	configVolume := name + "-config"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, _ = docker.ContainerRemove(cleanupCtx, name, client.ContainerRemoveOptions{Force: true})
		_, _ = docker.VolumeRemove(cleanupCtx, dataVolume, client.VolumeRemoveOptions{Force: true})
		_, _ = docker.VolumeRemove(cleanupCtx, configVolume, client.VolumeRemoveOptions{Force: true})
		if createdNetworkID != "" {
			_, _ = docker.NetworkRemove(cleanupCtx, createdNetworkID, client.NetworkRemoveOptions{})
		}
		_ = os.RemoveAll(root)
	})

	store, err := Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	socketPath := filepath.Join(root, "admin", "caddy-admin.sock")
	caddyDriver, err := NewCaddyGatewayDriver("unix://" + socketPath)
	if err != nil {
		t.Fatal(err)
	}
	provisioner := DockerGatewayProvisioner{
		Image:           defaultCaddyImage,
		Container:       name,
		DataVolume:      dataVolume,
		ConfigVolume:    configVolume,
		AdminListen:     caddyDriver.AdminListen,
		AdminSocketPath: caddyDriver.AdminSocketPath,
	}
	caddyDriver.SystemGateway = provisioner
	managed := &ManagedGatewayDriver{
		Caddy:   caddyDriver,
		Layer4:  &fakeLayer4Provisioner{},
		Runtime: provisioner,
	}

	httpPort := freeLoopbackPort(t)
	httpsPort := freeLoopbackPort(t)
	upstreamPort := freeLoopbackPort(t)
	desiredFor := func(revision int64, port int) gateway.DesiredState {
		return gateway.DesiredState{
			Revision:  revision,
			Listeners: []gateway.Listener{{Kind: "lan", Address: "127.0.0.1", HTTPPort: httpPort, HTTPSPort: httpsPort}},
			Routes: []gateway.Route{{
				ID:           "ci-probe-route",
				Hostname:     "probe.example.test",
				Protocol:     "http",
				ListenerKind: "lan",
				Upstreams:    []gateway.Upstream{{Address: "127.0.0.1", Port: port}},
			}},
		}
	}

	readLogs := func() realCaddyAdminLogCounts {
		t.Helper()
		stream, err := docker.ContainerLogs(ctx, name, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
		if err != nil {
			t.Fatalf("read Caddy container logs: %v", err)
		}
		defer stream.Close()
		var stdout, stderr bytes.Buffer
		if _, err := stdcopy.StdCopy(&stdout, &stderr, stream); err != nil {
			t.Fatalf("demux Caddy container logs: %v", err)
		}
		return countRealCaddyAdminLogs(stdout.Bytes(), stderr.Bytes())
	}

	readMetricsScrapeCount := func() int {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, caddyDriver.AdminURL+"/metrics", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := caddyDriver.client().Do(req)
		if err != nil {
			t.Fatalf("read /metrics: %v", err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, caddyAdminMetricsLimit))
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("/metrics failed: status=%d err=%v", resp.StatusCode, err)
		}
		return extractMetricsEndpointCount(raw)
	}

	waitForLog := func(description string, predicate func(realCaddyAdminLogCounts) bool) realCaddyAdminLogCounts {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			counts := readLogs()
			if predicate(counts) {
				return counts
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s; last counts=%+v", description, counts)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Phase 1: provision pinned caddy:2.11.4 container and apply rev 1.
	if err := applyGatewayDesiredState(ctx, store, managed, desiredFor(1, upstreamPort), nil); err != nil {
		t.Fatalf("apply rev 1 on pinned Caddy: %v", err)
	}
	afterApply := waitForLog("initial POST /load and GET /config/ INFO logs", func(c realCaddyAdminLogCounts) bool {
		return c.infoPostLoad >= 1 && c.infoGetConfig >= 1
	})
	if afterApply.infoGetMetrics != 0 {
		t.Fatalf("unexpected INFO log for /metrics: %+v", afterApply)
	}

	// Phase 2: 5 steady-state routine heartbeats must emit 0 new admin.api
	// INFO "received request" lines while actually scraping /metrics each time.
	metricsBefore := readMetricsScrapeCount()
	beforeSteady := readLogs()
	for i := range 5 {
		if err := managed.HeartbeatHealth(ctx); err != nil {
			t.Fatalf("steady-state heartbeat %d failed: %v", i+1, err)
		}
	}
	metricsAfter := readMetricsScrapeCount()
	afterSteady := readLogs()
	if afterSteady.infoReceived != beforeSteady.infoReceived {
		t.Fatalf("steady-state heartbeats emitted admin.api INFO logs: before=%+v after=%+v", beforeSteady, afterSteady)
	}
	if afterSteady.infoGetConfig != beforeSteady.infoGetConfig {
		t.Fatalf("steady-state heartbeats issued GET /config/: before=%+v after=%+v", beforeSteady, afterSteady)
	}
	if afterSteady.infoGetMetrics != 0 {
		t.Fatalf("/metrics emitted INFO logs on pinned Caddy: %+v", afterSteady)
	}
	// metricsAfter itself adds 1 scrape on top of the 5 heartbeat scrapes.
	if delta := metricsAfter - metricsBefore; delta < 6 {
		t.Fatalf("expected at least 5 heartbeat /metrics scrapes (+1 check), got delta=%d", delta)
	}

	// Phase 3: invalid POST /load (unknown app fails provisioning inside run())
	// preserves Admin write+error logs, sets reload_successful=0, and forces
	// immediate strict GET /config/ without caching on subsequent heartbeats.
	badBody := fmt.Sprintf(`{"admin":{"listen":%q},"apps":{"unknown_ci_app":{}}}`, caddyDriver.AdminListen)
	badReq, err := http.NewRequestWithContext(ctx, http.MethodPost, caddyDriver.AdminURL+"/load", strings.NewReader(badBody))
	if err != nil {
		t.Fatal(err)
	}
	badReq.Header.Set("Content-Type", "application/json")
	badResp, err := caddyDriver.client().Do(badReq)
	if err != nil || badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 on invalid /load, got resp=%v err=%v", badResp, err)
	}
	_ = badResp.Body.Close()
	afterBadLoad := waitForLog("preserved Admin write and error logs", func(c realCaddyAdminLogCounts) bool {
		return c.infoPostLoad > afterSteady.infoPostLoad && c.adminErrors > afterSteady.adminErrors
	})
	if err := managed.HeartbeatHealth(ctx); err != nil {
		t.Fatalf("failed reload with intact config reported unhealthy: %v", err)
	}
	afterFailedReloadCheck := waitForLog("strict GET /config/ after reload_successful=0", func(c realCaddyAdminLogCounts) bool {
		return c.infoGetConfig > afterBadLoad.infoGetConfig
	})
	if err := managed.HeartbeatHealth(ctx); err != nil {
		t.Fatalf("second heartbeat while reload_successful=0 failed: %v", err)
	}
	waitForLog("second strict GET /config/ while reload_successful=0", func(c realCaddyAdminLogCounts) bool {
		return c.infoGetConfig > afterFailedReloadCheck.infoGetConfig
	})

	// Phase 4: apply rev 2, verify steady silence, then externally wipe Caddy
	// to bootstrap-only config; the very next heartbeat must detect it.
	if err := applyGatewayDesiredState(ctx, store, managed, desiredFor(2, upstreamPort+1), nil); err != nil {
		t.Fatalf("apply rev 2 on pinned Caddy: %v", err)
	}
	beforeRev2Steady := readLogs()
	for i := range 2 {
		if err := managed.HeartbeatHealth(ctx); err != nil {
			t.Fatalf("rev 2 steady heartbeat %d failed: %v", i+1, err)
		}
	}
	if afterRev2Steady := readLogs(); afterRev2Steady.infoReceived != beforeRev2Steady.infoReceived {
		t.Fatalf("rev 2 steady heartbeats emitted INFO logs: before=%+v after=%+v", beforeRev2Steady, afterRev2Steady)
	}

	wipeBody := fmt.Sprintf(`{"admin":{"listen":%q,"config":{"persist":false}}}`, caddyDriver.AdminListen)
	wipeReq, err := http.NewRequestWithContext(ctx, http.MethodPost, caddyDriver.AdminURL+"/load", strings.NewReader(wipeBody))
	if err != nil {
		t.Fatal(err)
	}
	wipeReq.Header.Set("Content-Type", "application/json")
	wipeResp, err := caddyDriver.client().Do(wipeReq)
	if err != nil || wipeResp.StatusCode != http.StatusOK {
		t.Fatalf("external bootstrap wipe failed: resp=%v err=%v", wipeResp, err)
	}
	_ = wipeResp.Body.Close()
	if err := managed.HeartbeatHealth(ctx); err == nil || !strings.Contains(err.Error(), "Admin bootstrap") {
		t.Fatalf("external bootstrap wipe was not detected immediately: %v", err)
	}

	// Phase 5: apply rev 3, restart container, and confirm changed PID/start
	// time triggers an immediate strict GET /config/ before returning to
	// zero-INFO steady heartbeats.
	if err := applyGatewayDesiredState(ctx, store, managed, desiredFor(3, upstreamPort+2), nil); err != nil {
		t.Fatalf("apply rev 3 on pinned Caddy: %v", err)
	}
	beforeRestart := readLogs()
	if _, err := docker.ContainerRestart(ctx, name, client.ContainerRestartOptions{}); err != nil {
		t.Fatalf("restart Caddy container: %v", err)
	}
	if err := waitForCaddyAdminSocket(ctx, socketPath); err != nil {
		t.Fatalf("wait for Caddy Admin socket after restart: %v", err)
	}
	waitDeadline := time.Now().Add(15 * time.Second)
	for {
		if _, ok, transportErr := caddyDriver.readAdminMetricsGeneration(ctx); transportErr == nil && ok {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatal("restarted Caddy Admin /metrics did not become ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := managed.HeartbeatHealth(ctx); err != nil {
		t.Fatalf("post-restart heartbeat failed: %v", err)
	}
	afterRestart := waitForLog("strict GET /config/ after container restart", func(c realCaddyAdminLogCounts) bool {
		return c.infoGetConfig > beforeRestart.infoGetConfig
	})
	for i := range 3 {
		if err := managed.HeartbeatHealth(ctx); err != nil {
			t.Fatalf("post-restart steady heartbeat %d failed: %v", i+1, err)
		}
	}
	if afterRestartSteady := readLogs(); afterRestartSteady.infoReceived != afterRestart.infoReceived {
		t.Fatalf("post-restart steady heartbeats emitted INFO logs: before=%+v after=%+v", afterRestart, afterRestartSteady)
	}

	t.Logf("PASS pinned %s: 0 steady-state admin.api INFO logs, preserved POST /load + error logs, immediate detection on reload_successful=0, external wipe, and container restart", defaultCaddyImage)
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func countRealCaddyAdminLogs(streams ...[]byte) realCaddyAdminLogCounts {
	var counts realCaddyAdminLogCounts
	for _, stream := range streams {
		scanner := bufio.NewScanner(bytes.NewReader(stream))
		scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 || line[0] != '{' {
				continue
			}
			var entry struct {
				Level  string `json:"level"`
				Logger string `json:"logger"`
				Msg    string `json:"msg"`
				Method string `json:"method"`
				URI    string `json:"uri"`
			}
			if json.Unmarshal(line, &entry) != nil || entry.Logger != "admin.api" {
				continue
			}
			if entry.Level == "error" {
				counts.adminErrors++
			}
			if entry.Level == "info" && entry.Msg == "received request" {
				counts.infoReceived++
				if entry.Method == http.MethodGet && entry.URI == "/config/" {
					counts.infoGetConfig++
				}
				if entry.Method == http.MethodPost && entry.URI == "/load" {
					counts.infoPostLoad++
				}
				if entry.URI == "/metrics" {
					counts.infoGetMetrics++
				}
			}
		}
	}
	return counts
}

func extractMetricsEndpointCount(raw []byte) int {
	total := 0
	for len(raw) > 0 {
		var lineBytes []byte
		if idx := bytes.IndexByte(raw, '\n'); idx >= 0 {
			lineBytes = raw[:idx]
			raw = raw[idx+1:]
		} else {
			lineBytes = raw
			raw = nil
		}
		line := strings.TrimSpace(string(lineBytes))
		if !strings.HasPrefix(line, "caddy_admin_http_requests_total{") {
			continue
		}
		name, rest, ok := scanPrometheusMetricName(line)
		if !ok || name != "caddy_admin_http_requests_total" {
			continue
		}
		labels, rest, ok := parsePrometheusLabels(rest)
		if !ok || labels["method"] != http.MethodGet || labels["path"] != "/metrics" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		if v, err := strconv.ParseFloat(fields[0], 64); err == nil {
			total += int(v)
		}
	}
	return total
}
