package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/petauron/vastora/internal/agentpb"
	"github.com/petauron/vastora/internal/controlplane"
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
	observed := make(chan bool, 1)
	server := controlFixtureServer(&controlGRPCFixture{heartbeat: func(_ context.Context, input *agentpb.AgentDocument) (*agentpb.Document, error) {
		var body struct {
			GatewayHealthy              bool            `json:"gatewayHealthy"`
			NodeListenerHealthy         bool            `json:"nodeListenerHealthy"`
			RuntimeRecovery             json.RawMessage `json:"runtimeRecovery"`
			RuntimeRecoveryApplications json.RawMessage `json:"runtimeRecoveryApplications"`
		}
		if err := json.Unmarshal(input.Json, &body); err != nil {
			t.Error(err)
		}
		observed <- body.GatewayHealthy && body.NodeListenerHealthy
		if body.RuntimeRecovery != nil || body.RuntimeRecoveryApplications != nil {
			t.Error("heartbeat retained retired recovery protocol fields")
		}
		return fixtureDocument(t, map[string]any{}), nil
	}})
	defer server.Close()
	if err := store.SaveConnection(ctx, testConnection(t, "agent-1", "test", server.URL, "credential")); err != nil {
		t.Fatal(err)
	}
	// The new execution loop does not restore local desired state at startup.
	// Read-only health must not depend on the retired recovery flag.
	if err := (Client{GatewayDriver: driver}).StartupHeartbeat(ctx, store); err != nil {
		t.Fatal(err)
	}
	healthy := <-observed
	if !healthy || len(driver.appliedStates()) != 1 {
		t.Fatalf("heartbeat did not preserve read-only live observation: healthy=%v applies=%d", healthy, len(driver.appliedStates()))
	}
}

func TestHeartbeatReportsBlockedXrayRecovery(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.setApplicationRecovery(controlplane.RecoveryApplication{AppKey: threeXUIKey, ApplicationID: "application-1", Reason: "state_incomplete"})
	observed := make(chan bool, 1)
	server := controlFixtureServer(&controlGRPCFixture{heartbeat: func(_ context.Context, input *agentpb.AgentDocument) (*agentpb.Document, error) {
		var body struct {
			RuntimeRecovery             string                             `json:"runtimeRecovery"`
			RuntimeRecoveryApplications []controlplane.RecoveryApplication `json:"runtimeRecoveryApplications"`
		}
		if err := json.Unmarshal(input.Json, &body); err != nil {
			t.Error(err)
		}
		observed <- body.RuntimeRecovery == "application" && len(body.RuntimeRecoveryApplications) == 1 && body.RuntimeRecoveryApplications[0].ApplicationID == "application-1"
		return fixtureDocument(t, map[string]any{}), nil
	}})
	defer server.Close()
	ctx := context.Background()
	if err := store.SaveConnection(ctx, testConnection(t, "agent-1", "test", server.URL, "credential")); err != nil {
		t.Fatal(err)
	}
	if err := (Client{}).Heartbeat(ctx, store); err != nil {
		t.Fatal(err)
	}
	if !<-observed {
		t.Fatal("heartbeat omitted blocked Xray recovery")
	}
}
