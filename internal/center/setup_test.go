package center

import (
	"context"
	"strings"
	"testing"
)

func TestInitialSetupCreatesTheFirstRealSiteAndNetworkDefaults(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	status, err := store.SetupStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.AdministratorConfigured || status.OnboardingComplete {
		t.Fatalf("fresh Center is unexpectedly configured: %#v", status)
	}
	sites, err := store.ListSites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 0 {
		t.Fatalf("fresh Center created a placeholder site: %#v", sites)
	}
	if _, _, err := store.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple"); err != nil {
		t.Fatal(err)
	}
	status, err = store.SetupStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.AdministratorConfigured || status.OnboardingComplete {
		t.Fatalf("administrator creation incorrectly completed onboarding: %#v", status)
	}
	result, err := store.CompleteInitialSetup(ctx, InitialSetupInput{
		Site:    SiteInput{Name: "Singapore", Code: "singapore", Timezone: "Asia/Singapore", DomainSuffix: "sg.example.com"},
		Network: CenterNetworkInput{AgentConnectionMode: "public", AgentConnectURL: "https://center.example.com/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Site.Name != "Singapore" || result.Site.Timezone != "Asia/Singapore" || result.Network.AgentConnectURL != "https://center.example.com" {
		t.Fatalf("unexpected setup result: %#v", result)
	}
	status, err = store.SetupStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !status.OnboardingComplete {
		t.Fatalf("completed onboarding is not ready: %#v", status)
	}
	config, err := store.CenterNetworkConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if config.AgentConnectionMode != "public" || config.AgentConnectURL != "https://center.example.com" {
		t.Fatalf("unexpected saved network config: %#v", config)
	}
	if _, err := store.CreateAgentEnrollment(ctx, AgentEnrollmentSpec{Name: "missing-site", CenterURL: "https://center.example.com"}); err == nil {
		t.Fatal("Agent enrollment guessed a default site")
	}
	if _, err := store.CompleteInitialSetup(ctx, InitialSetupInput{Site: SiteInput{Name: "Duplicate", Code: "duplicate", Timezone: "UTC"}, Network: CenterNetworkInput{AgentConnectionMode: "lan", AgentConnectURL: "https://center.example.com"}}); err == nil {
		t.Fatal("initial setup completed twice")
	}
}

func TestInitialSetupValidatesAgentReachabilityAndHeadscale(t *testing.T) {
	for _, value := range []string{"https://center.example.com/path", "http://100.64.0.1:8080", "https://user:secret@center.example.com"} {
		if _, err := NormalizeAgentConnectURL(value); err == nil {
			t.Fatalf("unsafe Agent connection URL was accepted: %q", value)
		}
	}
	for _, value := range []string{"https://center.example.com", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		if _, err := NormalizeAgentConnectURL(value); err != nil {
			t.Fatalf("valid Agent connection URL %q was rejected: %v", value, err)
		}
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, _, err := store.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple"); err != nil {
		t.Fatal(err)
	}
	_, err = store.CompleteInitialSetup(ctx, InitialSetupInput{
		Site:    SiteInput{Name: "Private", Code: "private", Timezone: "UTC"},
		Network: CenterNetworkInput{AgentConnectionMode: "headscale", AgentConnectURL: "https://center.private.example.com"},
	})
	if err == nil || !strings.Contains(err.Error(), "configure Headscale") {
		t.Fatalf("unconfigured Headscale mode was accepted: %v", err)
	}
}

func testSiteID(t *testing.T, store *Store) string {
	t.Helper()
	sites, err := store.ListSites(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) > 0 {
		return sites[0].ID
	}
	site, err := store.CreateSite(context.Background(), SiteInput{Name: "Test", Code: "test", Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	return site.ID
}
