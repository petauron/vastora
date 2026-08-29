package center

import (
	"context"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestNetworkCandidatesAreCoLocatedOnlyForAssignedHostAddress(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.discoverNetworkCandidates = func(observedAt time.Time) ([]networking.Candidate, error) {
		return []networking.Candidate{
			{Address: "10.0.0.10", Interface: "ens3", Kind: networking.KindLAN, ObservedAt: observedAt},
			{Address: "100.64.0.1", Interface: "tailscale0", Kind: networking.KindHeadscale, ObservedAt: observedAt},
		}, nil
	}
	matched, err := store.networkCandidatesAreCoLocated([]networking.Candidate{{Address: "10.0.0.10", Interface: "ens3", Kind: networking.KindLAN}})
	if err != nil || !matched {
		t.Fatalf("co-located Agent not recognized: matched=%v err=%v", matched, err)
	}
	matched, err = store.networkCandidatesAreCoLocated([]networking.Candidate{{Address: "10.0.0.11", Interface: "ens3", Kind: networking.KindLAN}})
	if err != nil || matched {
		t.Fatalf("remote Agent was treated as co-located: matched=%v err=%v", matched, err)
	}
}

func TestCoLocatedHeadscaleAddressRequiresCurrentAgentCandidate(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	store.discoverNetworkCandidates = func(observedAt time.Time) ([]networking.Candidate, error) {
		return []networking.Candidate{{Address: "10.0.0.10", Interface: "ens3", Kind: networking.KindLAN, ObservedAt: observedAt}}, nil
	}
	site, err := store.CreateSite(ctx, SiteInput{Name: "Center", Code: "center", Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	timestamp := now.Format(time.RFC3339Nano)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO agents(id, name, credential_hash, version, status, enrolled_at, last_seen_at, site_id) VALUES('center-node', 'Center', X'01', 'test', 'active', ?, ?, ?)`, []any{timestamp, timestamp, site.ID}},
		{`INSERT INTO agent_network_profiles(agent_id, service_address, headscale_address, enabled_kinds_json, confirmed_at, candidate_observed_at) VALUES('center-node', '100.64.0.1', '100.64.0.1', '["headscale"]', ?, ?)`, []any{timestamp, timestamp}},
		{`INSERT INTO agent_network_candidates(agent_id, address, interface_name, kind, observed_at) VALUES('center-node', '10.0.0.10', 'ens3', 'lan', ?), ('center-node', '100.64.0.1', 'tailscale0', 'headscale', ?)`, []any{timestamp, timestamp}},
	}
	for _, statement := range statements {
		if _, err := store.db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	address, err := store.coLocatedHeadscaleAddress(ctx)
	if err != nil || address != "100.64.0.1" {
		t.Fatalf("current Headscale address = %q, err=%v", address, err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM agent_network_candidates WHERE kind = 'headscale'`); err != nil {
		t.Fatal(err)
	}
	address, err = store.coLocatedHeadscaleAddress(ctx)
	if err != nil || address != "" {
		t.Fatalf("stale Headscale address remained usable: %q, err=%v", address, err)
	}
}

func TestRestoredTailnetAddressQueuesPrivateGatewayListener(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "center-gateway", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.10", Interface: "ens3", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.10", LANAddress: "10.0.0.10", EnabledKinds: []string{networking.KindLAN}})
	if _, err := store.db.ExecContext(ctx, `UPDATE gateway_components SET status = 'ready', applied_generation = generation WHERE gateway_node_id = ?`, node.ID); err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := store.db.QueryRowContext(ctx, `SELECT generation FROM gateway_components WHERE gateway_node_id = ?`, node.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	heartbeat := NodeHeartbeat{
		Version: "test", Roles: []string{"worker", "gateway"}, Capabilities: NodeCapabilities{Docker: true, Gateway: true}, GatewayHealthy: true,
		NetworkCandidates: []networking.Candidate{
			{Address: "10.0.0.10", Interface: "ens3", Kind: networking.KindLAN},
			{Address: "100.64.0.1", Interface: "tailscale0", Kind: networking.KindHeadscale},
		},
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	var generation int64
	var status, lastError string
	if err := store.db.QueryRowContext(ctx, `SELECT generation, status, last_error FROM gateway_components WHERE gateway_node_id = ?`, node.ID).Scan(&generation, &status, &lastError); err != nil {
		t.Fatal(err)
	}
	if generation != before+1 || status != "failed" || lastError != "tailnet address became available; queued private listener reconcile" {
		t.Fatalf("restored tailnet reconcile = generation %d status %q error %q", generation, status, lastError)
	}
}
