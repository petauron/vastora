package center

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/gateway"
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
	configureBuiltinHeadscaleForTest(t, store)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?), (?, ?), (?, ?)`,
		agentConnectionModeSetting, "headscale",
		agentConnectURLSetting, "https://center.example.test",
		setupGatewayBindingSetting, `{"publicAddress":"192.9.143.79","bindAddress":"10.0.0.10"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE gateway_components SET status = 'ready', applied_generation = generation WHERE gateway_node_id = ?`, node.ID); err != nil {
		t.Fatal(err)
	}
	var beforeRevision int64
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision FROM gateway_states WHERE gateway_node_id = ?`, node.ID).Scan(&beforeRevision); err != nil {
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
	var revision int64
	var encoded []byte
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision, desired_json FROM gateway_states WHERE gateway_node_id = ?`, node.ID).Scan(&revision, &encoded); err != nil {
		t.Fatal(err)
	}
	if revision != beforeRevision+1 {
		t.Fatalf("restored tailnet desired revision = %d, want %d", revision, beforeRevision+1)
	}
	var desired gateway.DesiredState
	if err := json.Unmarshal(encoded, &desired); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, listener := range desired.Listeners {
		if listener.Kind == "headscale" && listener.Address == "100.64.0.1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("restored tailnet desired state is missing its private listener: %#v", desired)
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	var unchanged int64
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision FROM gateway_states WHERE gateway_node_id = ?`, node.ID).Scan(&unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged != revision {
		t.Fatalf("matching private listener was queued again: revision %d to %d", revision, unchanged)
	}
}

func TestUnhealthyGatewayRequeuesFullStateWithoutComponentRace(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "gateway-recovery", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.10", Interface: "ens3", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.10", LANAddress: "10.0.0.10", EnabledKinds: []string{networking.KindLAN}})
	if _, err := store.db.ExecContext(ctx, `UPDATE gateway_components SET status = 'ready', applied_generation = generation WHERE gateway_node_id = ?`, node.ID); err != nil {
		t.Fatal(err)
	}
	var generation, revision int64
	if err := store.db.QueryRowContext(ctx, `SELECT generation FROM gateway_components WHERE gateway_node_id = ?`, node.ID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision FROM gateway_states WHERE gateway_node_id = ?`, node.ID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE gateway_states SET applied_revision = desired_revision, status = 'ready' WHERE gateway_node_id = ?`, node.ID); err != nil {
		t.Fatal(err)
	}
	heartbeat := NodeHeartbeat{
		Version: "test", Roles: []string{"worker", "gateway"}, Capabilities: NodeCapabilities{Docker: true, Gateway: true}, GatewayHealthy: false,
		NetworkCandidates: []networking.Candidate{{Address: "10.0.0.10", Interface: "ens3", Kind: networking.KindLAN}},
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	var nextGeneration, nextRevision int64
	var componentStatus, stateStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT generation, status FROM gateway_components WHERE gateway_node_id = ?`, node.ID).Scan(&nextGeneration, &componentStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision, status FROM gateway_states WHERE gateway_node_id = ?`, node.ID).Scan(&nextRevision, &stateStatus); err != nil {
		t.Fatal(err)
	}
	if nextGeneration != generation || componentStatus != "ready" {
		t.Fatalf("unhealthy gateway component changed from generation %d to %d with status %q", generation, nextGeneration, componentStatus)
	}
	if nextRevision != revision+1 || stateStatus != "pending" {
		t.Fatalf("unhealthy gateway state = revision %d status %q, want revision %d pending", nextRevision, stateStatus, revision+1)
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	var unchangedRevision int64
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision FROM gateway_states WHERE gateway_node_id = ?`, node.ID).Scan(&unchangedRevision); err != nil {
		t.Fatal(err)
	}
	if unchangedRevision != nextRevision {
		t.Fatalf("pending gateway recovery queued again: revision %d to %d", nextRevision, unchangedRevision)
	}
}

func TestUnhealthyGatewayWithoutDesiredStateRequeuesComponent(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "gateway-bootstrap-recovery", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.10", Interface: "ens3", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.10", LANAddress: "10.0.0.10", EnabledKinds: []string{networking.KindLAN}})
	if _, err := store.db.ExecContext(ctx, `DELETE FROM gateway_states WHERE gateway_node_id = ?`, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE gateway_components SET status = 'ready', applied_generation = generation WHERE gateway_node_id = ?`, node.ID); err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err := store.db.QueryRowContext(ctx, `SELECT generation FROM gateway_components WHERE gateway_node_id = ?`, node.ID).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	heartbeat := NodeHeartbeat{
		Version: "test", Roles: []string{"worker", "gateway"}, Capabilities: NodeCapabilities{Docker: true, Gateway: true}, GatewayHealthy: false,
		NetworkCandidates: []networking.Candidate{{Address: "10.0.0.10", Interface: "ens3", Kind: networking.KindLAN}},
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	var nextGeneration int64
	var status, lastError string
	if err := store.db.QueryRowContext(ctx, `SELECT generation, status, last_error FROM gateway_components WHERE gateway_node_id = ?`, node.ID).Scan(&nextGeneration, &status, &lastError); err != nil {
		t.Fatal(err)
	}
	if nextGeneration != generation+1 || status != "failed" || lastError != "gateway health check failed; queued for reconcile" {
		t.Fatalf("unhealthy gateway bootstrap = generation %d status %q error %q", nextGeneration, status, lastError)
	}
}
