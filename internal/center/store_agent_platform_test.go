package center

import (
	"context"
	"strings"
	"testing"
)

func TestAgentEnrollmentPersistsSupportedPlatform(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	enrollment, err := store.CreateAgentEnrollment(context.Background(), AgentEnrollmentSpec{
		SiteID:    testSiteID(t, store),
		Name:      "arm-node",
		CenterURL: "https://center.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	credential, err := store.EnrollAgent(context.Background(), enrollment.Token, "test", "linux", "arm64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	agents, err := store.ListAgents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].ID != credential.ID || agents[0].OperatingSystem != "linux" || agents[0].Architecture != "arm64" {
		t.Fatalf("stored Agent platform = %#v", agents)
	}
}

func TestAgentEnrollmentRejectsUnsupportedPlatformWithoutConsumingToken(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	enrollment, err := store.CreateAgentEnrollment(context.Background(), AgentEnrollmentSpec{
		SiteID:    testSiteID(t, store),
		Name:      "unsupported-node",
		CenterURL: "https://center.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnrollAgent(context.Background(), enrollment.Token, "test", "linux", "386", testAgentPublicKey(t)); err == nil || !strings.Contains(err.Error(), "invalid Agent platform") {
		t.Fatalf("unsupported platform error = %v", err)
	}
	if _, err := store.EnrollAgent(context.Background(), enrollment.Token, "test", "linux", "amd64", testAgentPublicKey(t)); err != nil {
		t.Fatalf("unsupported platform consumed enrollment token: %v", err)
	}
}
