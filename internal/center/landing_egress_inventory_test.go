package center

import (
	"context"
	"encoding/json"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
	"testing"
)

func TestLandingEgressHeartbeatReplacesInventoryAndRejectsInvalidAddresses(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	candidates := []networking.Candidate{{Address: "10.0.0.93", Interface: "eth0", Kind: networking.KindLAN}}
	node := enrollOrchestrationNode(t, store, "egress", NodeCapabilities{Docker: true}, candidates, networking.Profile{ServiceAddress: "10.0.0.93", LANAddress: "10.0.0.93", EnabledKinds: []string{networking.KindLAN}})
	heartbeat := NodeHeartbeat{Version: "test", Roles: []string{"worker"}, Capabilities: NodeCapabilities{Docker: true, LandingEgressIP: true}, NetworkCandidates: candidates, LandingEgressAddresses: []landing.EgressAddress{{Address: "2001:4860:4860::8888", Interface: "eth0"}}}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	read := func() []landing.EgressAddress {
		t.Helper()
		var raw []byte
		if err := store.db.QueryRow(`SELECT landing_egress_addresses_json FROM agents WHERE id=?`, node.ID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var values []landing.EgressAddress
		if err := json.Unmarshal(raw, &values); err != nil {
			t.Fatal(err)
		}
		return values
	}
	if got := read(); len(got) != 1 || got[0].Address != heartbeat.LandingEgressAddresses[0].Address {
		t.Fatalf("missing v6: %+v", got)
	}
	heartbeat.LandingEgressAddresses = []landing.EgressAddress{{Address: "fe80::1", Interface: "eth0"}}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err == nil {
		t.Fatal("accepted link-local")
	}
	if len(read()) != 1 {
		t.Fatal("rejected report replaced inventory")
	}
	heartbeat.LandingEgressAddresses = nil
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	if len(read()) != 0 {
		t.Fatal("removed address remains selectable")
	}
}
