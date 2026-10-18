package center

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestLandingOnlyEnrollmentCapabilityValidation(t *testing.T) {
	for _, caps := range []NodeCapabilities{
		{LandingOnly: true, NativeEgress: true}, {Docker: true},
	} {
		encoded, _ := json.Marshal(caps)
		if err := decodeAgentEnrollmentRuntime([]byte(`["worker"]`), encoded, &AgentEnrollmentInstallProfile{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, caps := range []NodeCapabilities{
		{}, {LandingOnly: true}, {LandingOnly: true, NativeEgress: true, Docker: true},
		{LandingOnly: true, NativeEgress: true, Tunnel: true},
	} {
		encoded, _ := json.Marshal(caps)
		if err := decodeAgentEnrollmentRuntime([]byte(`["worker"]`), encoded, &AgentEnrollmentInstallProfile{}); err == nil {
			t.Fatalf("accepted invalid capabilities: %+v", caps)
		}
	}
}

func TestLandingOnlyInstallerSkipsContainerRuntime(t *testing.T) {
	profile := AgentEnrollmentInstallProfile{CenterURL: "https://center.example.com", Capabilities: NodeCapabilities{LandingOnly: true, NativeEgress: true}}
	script := renderAgentInstallScript(profile, profile.CenterURL)
	for _, forbidden := range []string{"command -v docker", "docker info", "/install/docker.sh", "docker.service", "docker.sock", "containerd"} {
		if strings.Contains(script, forbidden) || strings.Contains(renderAgentInstallLoader(profile.CenterURL), forbidden) {
			t.Fatalf("landing-only bootstrap touches container runtime: %s", forbidden)
		}
	}
	for _, required := range []string{"sha256sum", "failed its version check", "amd64|arm64", "debian:12", "debian:13", "ubuntu:22.04", "ubuntu:24.04", "ubuntu:26.04"} {
		if !strings.Contains(script, required) {
			t.Fatalf("landing-only bootstrap lost %s", required)
		}
	}
	profile.Capabilities = NodeCapabilities{Docker: true}
	if !strings.Contains(renderAgentInstallScript(profile, profile.CenterURL), "docker info") {
		t.Fatal("ordinary installation lost Docker verification")
	}
}

func TestLandingOnlyRejectsInvalidPurposeAndHeartbeatEscalation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, spec := range []AgentEnrollmentSpec{
		{LandingOnly: true}, {LandingOnly: true, UseHeadscale: true, Gateway: true}, {LandingOnly: true, UseHeadscale: true, Tunnel: true},
	} {
		if _, err := store.CreateAgentEnrollment(ctx, spec); err == nil {
			t.Fatal("accepted invalid purpose")
		}
	}
	enrollment, err := store.CreateAgentEnrollment(ctx, AgentEnrollmentSpec{SiteID: testSiteID(t, store), Name: "ordinary", CenterURL: "https://center.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.EnrollAgent(ctx, enrollment.Token, "test", "linux", "amd64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{Version: "test", Roles: []string{"worker"}, Capabilities: NodeCapabilities{LandingOnly: true, NativeEgress: true}}); err == nil {
		t.Fatal("heartbeat converted ordinary node to landing-only")
	}
	// Seed the authorized profile to test the opposite boundary independently
	// of Headscale's remote enrollment integration.
	caps, _ := json.Marshal(NodeCapabilities{LandingOnly: true, NativeEgress: true})
	if _, err := store.db.Exec(`UPDATE agents SET capabilities_json=? WHERE id=?`, caps, node.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{Version: "test", Roles: []string{"worker"}, Capabilities: NodeCapabilities{Docker: true}}); err == nil {
		t.Fatal("heartbeat escalated landing-only node")
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/meridian"}); err == nil || !strings.Contains(err.Error(), "landing-only") {
		t.Fatalf("application not rejected by purpose: %v", err)
	}
}
