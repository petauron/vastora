package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/gateway"
)

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
		_, _ = response.Write([]byte(`{"success":true,"obj":[{"id":7,"remark":"vless","protocol":"vless","port":443,"listen":"0.0.0.0","enable":true,"streamSettings":{"network":"tcp"}},{"id":8,"remark":"disabled","protocol":"vmess","port":8443,"listen":"127.0.0.1","enable":false,"streamSettings":{"network":"ws"}}]}`))
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
	if observed[0].Name != "inbound-7" || observed[0].AppProtocol != "vless/tcp" || observed[0].Protocol != "tcp" || !observed[0].Enabled || observed[0].Port != 443 {
		t.Fatalf("enabled inbound was not synchronized: %#v", observed[0])
	}
	if observed[1].Name != "inbound-8" || observed[1].AppProtocol != "vmess/ws" || observed[1].Enabled {
		t.Fatalf("disabled inbound state was not preserved: %#v", observed[1])
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
	if _, err := store.RecordGatewayState(context.Background(), state); err != nil {
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
