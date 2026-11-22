package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/petauron/vastora/internal/agentpb"
)

func TestLandingOnlyHeartbeatEncodesAbsentTailscaleOwnership(t *testing.T) {
	server := controlFixtureServer(&controlGRPCFixture{heartbeat: func(_ context.Context, input *agentpb.AgentDocument) (*agentpb.Document, error) {
		var payload struct {
			TailscaleOwnership string       `json:"tailscaleOwnership"`
			Capabilities       Capabilities `json:"capabilities"`
		}
		if err := json.Unmarshal(input.Json, &payload); err != nil {
			t.Error(err)
		}
		if payload.TailscaleOwnership != "" || !payload.Capabilities.LandingOnly || payload.Capabilities.Docker {
			t.Errorf("invalid landing heartbeat: %+v", payload)
		}
		return fixtureDocument(t, map[string]any{}), nil
	}})
	defer server.Close()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.SaveConnection(context.Background(), testConnection(t, "agent-1", "lab", server.URL, "credential")); err != nil {
		t.Fatal(err)
	}
	if err := (Client{Capabilities: Capabilities{LandingOnly: true}, TailscaleOwnership: "none"}).Heartbeat(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

func TestLandingOnlyPurposePersistsAndRejectsLocalEscalation(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	landing := Capabilities{LandingOnly: true, NativeEgress: true}
	if err := store.ValidateNodePurpose(ctx, landing); err == nil {
		t.Fatal("accepted unauthorised local landing flag")
	}
	op, err := store.BeginEnrollmentOperation(ctx, "http://127.0.0.1:8080", "token", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteEnrollmentOperation(ctx, op, Enrollment{ID: "landing-node", Name: "landing", Credential: "credential", Roles: []string{"worker"}, Capabilities: landing}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agent_install_operations SET phase='healthy' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteInstallOperation(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ValidateNodePurpose(ctx, landing); err != nil {
		t.Fatal(err)
	}
	for _, caps := range []Capabilities{{Docker: true}, {LandingOnly: true, Docker: true}, {LandingOnly: true, Gateway: true}, {LandingOnly: true, Tunnel: true}} {
		if err := store.ValidateNodePurpose(ctx, caps); err == nil {
			t.Fatalf("accepted local escalation: %+v", caps)
		}
	}
	summary, err := InspectConnection(directory)
	if err != nil || !summary.LandingOnly {
		t.Fatalf("uninstall inspection lost purpose: %+v %v", summary, err)
	}
}

func TestLandingOnlyTaskAllowlist(t *testing.T) {
	for _, kind := range []string{"agent.update", "agent.decommission", "node.security", "node.swap"} {
		if !landingOnlyTaskAllowed(kind) {
			t.Fatalf("required lifecycle task blocked: %s", kind)
		}
	}
	for _, kind := range []string{"application.apply", "application.adopt", "application.command", "application.maintenance", "gateway.routes.apply", "gateway.component.apply", "tunnel.state.apply", "node.listener.apply", "landing.server.apply", "landing.proxy.apply", "unknown"} {
		if landingOnlyTaskAllowed(kind) {
			t.Fatalf("unauthorized task allowed: %s", kind)
		}
	}
}
