package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExecutionHeartbeatObservesHealthyIngressWithoutStartupReplay(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	driver := &fakeGatewayDriver{}
	if err := applyGatewayDesiredState(ctx, store, driver, gatewayState(1, 3000), nil); err != nil {
		t.Fatal(err)
	}
	observed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			GatewayHealthy              bool            `json:"gatewayHealthy"`
			NodeListenerHealthy         bool            `json:"nodeListenerHealthy"`
			RuntimeRecovery             json.RawMessage `json:"runtimeRecovery"`
			RuntimeRecoveryApplications json.RawMessage `json:"runtimeRecoveryApplications"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		observed = body.GatewayHealthy && body.NodeListenerHealthy
		if body.RuntimeRecovery != nil || body.RuntimeRecoveryApplications != nil {
			t.Error("heartbeat retained retired recovery protocol fields")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer server.Close()
	if err := store.SaveConnection(ctx, testConnection(t, "agent-1", "test", server.URL, "credential")); err != nil {
		t.Fatal(err)
	}
	// The new execution loop does not restore local desired state at startup.
	// Read-only health must not depend on the retired recovery flag.
	if err := (Client{GatewayDriver: driver}).StartupHeartbeat(ctx, store); err != nil {
		t.Fatal(err)
	}
	if !observed || len(driver.appliedStates()) != 1 {
		t.Fatalf("heartbeat did not preserve read-only live observation: healthy=%v applies=%d", observed, len(driver.appliedStates()))
	}
}
