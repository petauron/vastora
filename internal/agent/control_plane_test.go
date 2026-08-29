package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/gateway"
)

type fakeHostDecommissioner struct {
	prepared   bool
	scheduled  bool
	deleteData bool
}

func (d *fakeHostDecommissioner) Prepare(_ context.Context, deleteData bool) error {
	d.prepared = true
	d.deleteData = deleteData
	return nil
}

func (d *fakeHostDecommissioner) ScheduleFinalRemoval(_ context.Context, deleteData bool) error {
	d.scheduled = true
	if deleteData != d.deleteData {
		return errors.New("delete-data changed between cleanup phases")
	}
	return nil
}

func TestEnrollmentReportsNativePlatform(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var input struct {
			Token           string `json:"token"`
			Version         string `json:"version"`
			OperatingSystem string `json:"operatingSystem"`
			Architecture    string `json:"architecture"`
		}
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/agents/enroll" || json.NewDecoder(request.Body).Decode(&input) != nil {
			t.Fatalf("unexpected enrollment request: %s %s", request.Method, request.URL.Path)
		}
		if input.Token != "one-time-token" || input.Version != Version || input.OperatingSystem != runtime.GOOS || input.Architecture != runtime.GOARCH {
			t.Fatalf("enrollment platform payload = %#v", input)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":"agent-1","credential":"credential","name":"arm-node","roles":["worker"],"capabilities":{"docker":true}}`))
	}))
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := (Client{HTTPClient: server.Client()}).Enroll(context.Background(), store, server.URL, "one-time-token"); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateEnrollmentReplacesOnlyCenterConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"id":"new-agent","credential":"new-credential","name":"moved-node","roles":["worker","gateway"],"capabilities":{"docker":true,"gateway":true}}`))
	}))
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), Connection{AgentID: "old-agent", Name: "old-node", CenterURL: "https://old-center.example.com", Credential: "old-credential"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordApplied(context.Background(), AppliedInstallation{InstanceID: "existing-app", AppKey: "vastora-official/cpa", Version: "1.0.0", Config: json.RawMessage(`{}`), Secrets: json.RawMessage(`{}`), ServiceAddress: "100.64.0.2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (Client{HTTPClient: server.Client()}).MigrateEnrollment(context.Background(), store, server.URL, "one-time-token"); err != nil {
		t.Fatal(err)
	}
	connection, err := store.Connection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if connection.AgentID != "new-agent" || connection.Name != "moved-node" || connection.CenterURL != server.URL || connection.Credential != "new-credential" {
		t.Fatalf("migrated connection = %#v", connection)
	}
	installations, err := store.ListApplied(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(installations) != 1 || installations[0].InstanceID != "existing-app" {
		t.Fatalf("migration changed local application state: %#v", installations)
	}
}

func TestObserveThreeXUISynchronizesEnabledInboundsWithoutChangingThem(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestCount++
		if request.Method != http.MethodGet || request.URL.Path != "/panel/api/inbounds/list" {
			t.Fatalf("unexpected 3x-ui API request: %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer local-token" {
			t.Fatalf("missing 3x-ui Bearer token: %q", request.Header.Get("Authorization"))
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"success":true,"obj":[{"id":7,"remark":"vless","protocol":"vless","port":443,"listen":"0.0.0.0","enable":true,"tag":"vastora-reality-7","total":1073741824,"streamSettings":{"network":"tcp","security":"reality"}},{"id":8,"remark":"disabled","protocol":"vmess","port":8443,"listen":"127.0.0.1","enable":false,"streamSettings":{"network":"ws"}}]}`))
	}))
	defer server.Close()
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	config, _ := json.Marshal(map[string]any{"timezone": "UTC", "panel_port": port, "enable_fail2ban": true, "vmess_aead_forced": false})
	if _, err := store.RecordApplied(context.Background(), AppliedInstallation{InstanceID: "3x-install", AppKey: threeXUIKey, Version: "3.6.0", Config: config, Secrets: json.RawMessage(`{"api_token":"local-token"}`), ServiceAddress: host}); err != nil {
		t.Fatal(err)
	}

	observed, err := observeThreeXUI(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if requestCount != 1 || len(observed) != 2 {
		t.Fatalf("unexpected 3x-ui observation: requests=%d values=%#v", requestCount, observed)
	}
	if observed[0].Name != "inbound-7" || observed[0].AppProtocol != "vless/tcp/reality" || observed[0].Protocol != "tcp" || !observed[0].Enabled || observed[0].Port != 443 || observed[0].InboundTag != "vastora-reality-7" || observed[0].InboundTotalBytes != 1073741824 {
		t.Fatalf("enabled inbound was not synchronized: %#v", observed[0])
	}
	if observed[1].Name != "inbound-8" || observed[1].AppProtocol != "vmess/ws" || observed[1].Enabled {
		t.Fatalf("disabled inbound state was not preserved: %#v", observed[1])
	}
}

func TestThreeXUIClientInboundDisplayNameSurvivesAgentRoundTrip(t *testing.T) {
	var task ThreeXUIClientCommandTask
	if err := json.Unmarshal([]byte(`{"action":"list","inbounds":[{"id":7,"name":"inbound-7","displayName":"美国CloudLead","applicationId":"app-1","nodeId":"node-1","nodeName":"CloudLead"}]}`), &task); err != nil {
		t.Fatal(err)
	}
	result := ThreeXUIClientCommandResult{Inbounds: task.Inbounds}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"displayName":"美国CloudLead"`) {
		t.Fatalf("display name was lost from Agent result: %s", payload)
	}
}

func TestDeferredTaskCompletionErrorKeepsItsCause(t *testing.T) {
	cause := errors.New("external cleanup outcome is unknown")
	deferred := deferTaskCompletion(cause)
	if !taskCompletionIsDeferred(deferred) || !errors.Is(deferred, cause) || deferred.Error() != cause.Error() {
		t.Fatalf("deferred task error = %#v", deferred)
	}
	if taskCompletionIsDeferred(cause) || taskCompletionIsDeferred(nil) {
		t.Fatal("ordinary task errors were incorrectly deferred")
	}
	if !taskCompletionShouldBeDeferred(deferred, 1) || taskCompletionShouldBeDeferred(deferred, maxDeferredTaskAttempts) {
		t.Fatal("deferred task completion did not honor its bounded attempt budget")
	}
	reconciliation := deferTaskUntilReconciled(cause)
	if !errors.Is(reconciliation, cause) || !taskCompletionShouldBeDeferred(reconciliation, 1) || taskCompletionShouldBeDeferred(reconciliation, maxDeferredTaskAttempts) || !taskCompletionRequiresReconciliation(reconciliation, maxDeferredTaskAttempts) {
		t.Fatal("externally committed task did not defer and then quarantine at its retry limit")
	}
}

func TestHeartbeatsRestoreGatewayStateOnlyAtStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	heartbeats := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/agents/agent-1/heartbeat":
			heartbeats++
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{}`))
			if heartbeats == 2 {
				cancel()
			}
		case "/api/v1/agents/agent-1/tasks/next":
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"task":null}`))
		default:
			t.Fatalf("unexpected request path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), Connection{AgentID: "agent-1", Name: "test", CenterURL: server.URL, Credential: "credential"}); err != nil {
		t.Fatal(err)
	}
	state := gateway.DesiredState{Revision: 3, Listeners: []gateway.Listener{{Kind: "lan", Address: "192.0.2.10", HTTPPort: 80, HTTPSPort: 443}}}
	if _, err := store.RecordGatewayState(context.Background(), state, nil); err != nil {
		t.Fatal(err)
	}
	driver := &fakeGatewayDriver{}
	(Client{GatewayDriver: driver}).RunHeartbeats(ctx, store, time.Second, func(err error) {
		if ctx.Err() == nil {
			t.Errorf("heartbeat error: %v", err)
		}
	})
	if heartbeats != 2 {
		t.Fatalf("expected two heartbeat cycles, got %d", heartbeats)
	}
	if len(driver.applied) != 1 || driver.applied[0].Revision != 3 {
		t.Fatalf("persisted gateway state was restored %d times: %#v", len(driver.applied), driver.applied)
	}
}

func TestTaskClaimUsesBoundedLongPoll(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/agents/agent-1/tasks/next" || request.URL.Query().Get("wait") != "10s" {
			t.Fatalf("unexpected task claim request: %s", request.URL.String())
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"task":null}`))
	}))
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), Connection{AgentID: "agent-1", Name: "test", CenterURL: server.URL, Credential: "credential"}); err != nil {
		t.Fatal(err)
	}
	task, err := (Client{}).claimNextTask(context.Background(), store, 10*time.Second)
	if err != nil || task != nil {
		t.Fatalf("unexpected task claim result: task=%#v err=%v", task, err)
	}
}

func TestHeartbeatKeepsCenterConnectedWhenThreeXUIObservationFails(t *testing.T) {
	heartbeats := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/agents/agent-1/heartbeat" {
			t.Fatalf("unexpected request path: %s", request.URL.Path)
		}
		var payload struct {
			ApplicationEndpoints         []ApplicationEndpointObservation `json:"applicationEndpoints"`
			ApplicationEndpointsObserved bool                             `json:"applicationEndpointsObserved"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.ApplicationEndpointsObserved || len(payload.ApplicationEndpoints) != 0 {
			t.Fatalf("failed observation was reported as authoritative: %#v", payload)
		}
		heartbeats++
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{}`))
	}))
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), Connection{AgentID: "agent-1", Name: "test", CenterURL: server.URL, Credential: "credential"}); err != nil {
		t.Fatal(err)
	}
	config := json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`)
	if _, err := store.RecordApplied(context.Background(), AppliedInstallation{InstanceID: "3x-install", AppKey: threeXUIKey, Version: "3.6.0", Config: config, Secrets: json.RawMessage(`{}`), ServiceAddress: "127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	observationErr, heartbeatErr := (Client{}).heartbeat(context.Background(), store)
	if heartbeatErr != nil {
		t.Fatalf("observation failure blocked heartbeat: %v", heartbeatErr)
	}
	if observationErr == nil || heartbeats != 1 {
		t.Fatalf("observation error was not isolated: err=%v heartbeats=%d", observationErr, heartbeats)
	}
}

func TestInitialHeartbeatRequestsRealityGuardRevalidation(t *testing.T) {
	startup := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var payload struct {
			Startup bool `json:"startup"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		startup = payload.Startup
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{}`))
	}))
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), Connection{AgentID: "agent-1", Name: "test", CenterURL: server.URL, Credential: "credential"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (Client{}).heartbeatWithStartup(context.Background(), store, true); err != nil {
		t.Fatal(err)
	}
	if !startup {
		t.Fatal("initial heartbeat did not request REALITY guard revalidation")
	}
}

func TestHeartbeatSwitchesToVerifiedCenterURL(t *testing.T) {
	healthChecks := 0
	newCenter := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/healthz" {
			t.Fatalf("unexpected new Center request path: %s", request.URL.Path)
		}
		healthChecks++
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"status":"ok"}`))
	}))
	defer newCenter.Close()

	oldCenter := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/agents/agent-1/heartbeat" {
			t.Fatalf("unexpected old Center request path: %s", request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{"connected": true, "centerUrl": newCenter.URL})
	}))
	defer oldCenter.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), Connection{AgentID: "agent-1", Name: "test", CenterURL: oldCenter.URL, Credential: "credential"}); err != nil {
		t.Fatal(err)
	}
	observationErr, heartbeatErr := (Client{}).heartbeat(context.Background(), store)
	if observationErr != nil || heartbeatErr != nil {
		t.Fatalf("heartbeat failed: observation=%v heartbeat=%v", observationErr, heartbeatErr)
	}
	connection, err := store.Connection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if connection.CenterURL != newCenter.URL || healthChecks != 1 {
		t.Fatalf("Center URL was not switched after verification: connection=%#v healthChecks=%d", connection, healthChecks)
	}
}

func TestHeartbeatKeepsExplicitHostOnlyCenterChannel(t *testing.T) {
	healthChecks := 0
	newCenter := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { healthChecks++ }))
	defer newCenter.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	connection := Connection{AgentID: "agent-1", Name: "center-host", CenterURL: "http://127.0.0.1:8080", Credential: "credential"}
	if err := store.SaveConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLocalCenterChannel(connection.CenterURL); err != nil {
		t.Fatal(err)
	}
	if err := (Client{}).applyDesiredCenterURL(context.Background(), store, connection, newCenter.URL); err != nil {
		t.Fatal(err)
	}
	current, err := store.Connection(context.Background())
	if err != nil || current.CenterURL != connection.CenterURL || healthChecks != 0 {
		t.Fatalf("host-only channel changed: connection=%#v healthChecks=%d err=%v", current, healthChecks, err)
	}
}

func TestHeartbeatKeepsCurrentCenterWhenDesiredURLIsNotReady(t *testing.T) {
	newCenter := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"status":"reconciling"}`))
	}))
	defer newCenter.Close()
	oldCenter := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{"connected": true, "centerUrl": newCenter.URL})
	}))
	defer oldCenter.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), Connection{AgentID: "agent-1", Name: "test", CenterURL: oldCenter.URL, Credential: "credential"}); err != nil {
		t.Fatal(err)
	}
	_, heartbeatErr := (Client{}).heartbeat(context.Background(), store)
	if heartbeatErr == nil || !strings.Contains(heartbeatErr.Error(), "health check is not OK") {
		t.Fatalf("unready Center URL was not rejected: %v", heartbeatErr)
	}
	connection, err := store.Connection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if connection.CenterURL != oldCenter.URL {
		t.Fatalf("unready Center URL replaced the working address: %#v", connection)
	}
}

func TestHeartbeatAppliesCenterTailscaleIsolationState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/agents/agent-1/heartbeat" {
			t.Fatalf("unexpected request path: %s", request.URL.Path)
		}
		var heartbeat struct {
			TailscaleEnrolled  bool   `json:"tailscaleEnrolled"`
			TailscaleOwnership string `json:"tailscaleOwnership"`
		}
		if err := json.NewDecoder(request.Body).Decode(&heartbeat); err != nil {
			t.Fatal(err)
		}
		if !heartbeat.TailscaleEnrolled || heartbeat.TailscaleOwnership != "managed" {
			t.Fatalf("Tailscale host state was not reported: %#v", heartbeat)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"tailscaleIsolation":{"controlUrl":"https://headscale.example.com","controlAddresses":["203.0.113.10"]}}`))
	}))
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), Connection{AgentID: "agent-1", Name: "test", CenterURL: server.URL, Credential: "credential"}); err != nil {
		t.Fatal(err)
	}
	var applied TailscaleIsolationDesiredState
	client := Client{TailscaleEnrolled: true, TailscaleOwnership: "managed", TailscaleIsolation: func(_ context.Context, state TailscaleIsolationDesiredState) error {
		applied = state
		return nil
	}}
	if _, err := client.heartbeat(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if applied.ControlURL != "https://headscale.example.com" || len(applied.ControlAddresses) != 1 || applied.ControlAddresses[0] != "203.0.113.10" {
		t.Fatalf("Tailscale isolation state was not applied: %#v", applied)
	}
}

func TestTunnelDesiredStateRequiresFixedImageAndTokenWhileRunning(t *testing.T) {
	valid := TunnelDesiredState{Revision: 2, Status: "running", Image: "docker.io/cloudflare/cloudflared:2026.7.2", Token: "token"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid Tunnel state was rejected: %v", err)
	}
	valid.Token = ""
	if err := valid.Validate(); err == nil {
		t.Fatal("running Tunnel without a token was accepted")
	}
	if err := (TunnelDesiredState{Revision: 3, Status: "stopped"}).Validate(); err != nil {
		t.Fatalf("stopped Tunnel unexpectedly required a token: %v", err)
	}
}

func TestAgentDecommissionSchedulesFinalRemovalBeforeAcknowledgement(t *testing.T) {
	acknowledged := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/agents/agent-1/tasks/agent-decommission-agent-1/result" || request.Header.Get("Authorization") != "Bearer credential" {
			t.Fatalf("unexpected task acknowledgement: %s %s", request.Method, request.URL.Path)
		}
		acknowledged = true
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), Connection{AgentID: "agent-1", Name: "node", CenterURL: server.URL, Credential: "credential"}); err != nil {
		t.Fatal(err)
	}
	decommissioner := &fakeHostDecommissioner{}
	(Client{HTTPClient: server.Client(), Decommissioner: decommissioner}).processTask(context.Background(), store, DeploymentTask{
		Kind: "agent.decommission", ID: "agent-decommission-agent-1", Attempt: 1, DeleteData: true,
	}, func(err error) { t.Errorf("task error: %v", err) })
	if !decommissioner.prepared || !decommissioner.scheduled || !acknowledged {
		t.Fatalf("decommission lifecycle incomplete: %#v acknowledged=%v", decommissioner, acknowledged)
	}
}
