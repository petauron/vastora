package center

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestFixedTailscaleEndpointRequiresExplicitConfirmedChoice(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	store.discoverNetworkCandidates = func(observedAt time.Time) ([]networking.Candidate, error) {
		return []networking.Candidate{{Address: "10.0.0.10", Interface: "ens3", Kind: networking.KindLAN, ObservedAt: observedAt}}, nil
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO network_integrations(kind, mode, endpoint, status, created_at, updated_at) VALUES('headscale', 'builtin', 'https://headscale.example.com', 'configured', ?, ?)`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?)`, setupGatewayBindingSetting, `{"publicAddress":"8.8.8.8","bindAddress":"10.0.0.10"}`); err != nil {
		t.Fatal(err)
	}
	site, err := store.CreateSite(ctx, SiteInput{Name: "Center", Code: "center", Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO agents(id, name, credential_hash, version, status, enrolled_at, last_seen_at, site_id, tailscale_ownership) VALUES('center-agent', 'Center', X'0102', 'test', 'active', ?, ?, ?, 'managed')`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO agent_network_candidates(agent_id, address, interface_name, kind, observed_at) VALUES('center-agent', '10.0.0.10', 'ens3', 'lan', ?)`, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	state, err := store.tailscaleIsolationDesiredState(ctx, "center-agent")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.StaticEndpoints) != 0 {
		t.Fatalf("public-entry detection enabled a fixed endpoint: %#v", state)
	}
	for _, input := range []TailscaleFixedEndpointInput{
		{Enabled: true, Endpoint: "8.8.8.8:41641", LocalAddress: "10.0.0.10"},
		{Enabled: true, Endpoint: "8.8.8.8:443", LocalAddress: "10.0.0.10", ConfirmMapping: true},
		{Enabled: true, Endpoint: "10.0.0.20:41641", LocalAddress: "10.0.0.10", ConfirmMapping: true},
		{Enabled: true, Endpoint: "8.8.8.8:41641", LocalAddress: "10.0.0.20", ConfirmMapping: true},
	} {
		if _, err := store.ConfigureTailscaleFixedEndpoint(ctx, input); err == nil {
			t.Fatalf("invalid fixed endpoint was accepted: %#v", input)
		}
	}
	view, err := store.ConfigureTailscaleFixedEndpoint(ctx, TailscaleFixedEndpointInput{Enabled: true, Endpoint: "8.8.8.8:41641", LocalAddress: "10.0.0.10", ConfirmMapping: true})
	if err != nil {
		t.Fatal(err)
	}
	if !view.Available || !view.Enabled || view.Status != "configured" {
		t.Fatalf("saved fixed endpoint view = %#v", view)
	}
	state, err = store.tailscaleIsolationDesiredState(ctx, "center-agent")
	if err != nil || len(state.StaticEndpoints) != 1 || state.StaticEndpoints[0] != "8.8.8.8:41641" {
		t.Fatalf("fixed endpoint desired state = %#v, err=%v", state, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE agents SET tailscale_ownership = 'external' WHERE id = 'center-agent'`); err != nil {
		t.Fatal(err)
	}
	view, err = store.TailscaleFixedEndpoint(ctx)
	if err != nil || view.Available || view.Status != "unavailable" {
		t.Fatalf("user-managed Tailscale exposed the fixed-endpoint setting: %#v, err=%v", view, err)
	}
	state, err = store.tailscaleIsolationDesiredState(ctx, "center-agent")
	if err != nil || len(state.StaticEndpoints) != 0 {
		t.Fatalf("user-managed Tailscale received a fixed endpoint: %#v, err=%v", state, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE agents SET tailscale_ownership = 'managed' WHERE id = 'center-agent'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM agent_network_candidates WHERE agent_id = 'center-agent'`); err != nil {
		t.Fatal(err)
	}
	state, err = store.tailscaleIsolationDesiredState(ctx, "center-agent")
	if err != nil || len(state.StaticEndpoints) != 0 {
		t.Fatalf("stale local mapping remained advertised: %#v, err=%v", state, err)
	}
	store.discoverNetworkCandidates = func(time.Time) ([]networking.Candidate, error) { return nil, nil }
	view, err = store.TailscaleFixedEndpoint(ctx)
	if err != nil || view.Status != "action_required" || !strings.Contains(view.LastError, "no longer present") {
		t.Fatalf("stale fixed endpoint view = %#v, err=%v", view, err)
	}
	view, err = store.ConfigureTailscaleFixedEndpoint(ctx, TailscaleFixedEndpointInput{Enabled: false})
	if err != nil || view.Enabled || view.Status != "disabled" {
		t.Fatalf("fixed endpoint disable = %#v, err=%v", view, err)
	}
}

func TestExternalHeadscaleDoesNotOfferManagedFixedEndpoint(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO network_integrations(kind, mode, endpoint, status, created_at, updated_at) VALUES('headscale', 'external', 'https://headscale.example.com', 'configured', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	view, err := store.TailscaleFixedEndpoint(context.Background())
	if err != nil || view.Available || view.Status != "unavailable" {
		t.Fatalf("external Headscale fixed endpoint view = %#v, err=%v", view, err)
	}
	if _, err := store.ConfigureTailscaleFixedEndpoint(context.Background(), TailscaleFixedEndpointInput{Enabled: true, Endpoint: "203.0.113.10:41641", LocalAddress: "10.0.0.10", ConfirmMapping: true}); err == nil {
		t.Fatal("external Headscale accepted a managed fixed endpoint")
	}
}
