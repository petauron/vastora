package center

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/petauron/vastora/internal/networking"
)

func TestEmptyGatewayHeartbeatsDoNotQueueRoutes(t *testing.T) {
	for _, desiredStatus := range []string{"running", "stopped", "absent"} {
		t.Run(desiredStatus, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			candidates := []networking.Candidate{{Address: "100.64.0.40", Interface: "tailscale0", Kind: networking.KindHeadscale}}
			node := enrollOrchestrationNode(t, store, "empty-gateway", NodeCapabilities{Gateway: true}, candidates, networking.Profile{ServiceAddress: "100.64.0.40", HeadscaleAddress: "100.64.0.40", EnabledKinds: []string{networking.KindHeadscale}})
			if desiredStatus == "absent" {
				if _, err := store.db.Exec(`DELETE FROM gateway_components WHERE gateway_node_id=?`, node.ID); err != nil {
					t.Fatal(err)
				}
			} else if _, err := store.db.Exec(`UPDATE gateway_components SET desired_status=? WHERE gateway_node_id=?`, desiredStatus, node.ID); err != nil {
				t.Fatal(err)
			}
			var before, after int
			if err := store.db.QueryRow(`SELECT count(*) FROM task_events WHERE kind='gateway.routes.apply'`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			for range 8 {
				if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{Version: "test", Roles: []string{"gateway"}, Capabilities: NodeCapabilities{Gateway: true}, GatewayHealthy: true, NetworkCandidates: candidates}); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.db.QueryRow(`SELECT count(*) FROM task_events WHERE kind='gateway.routes.apply'`).Scan(&after); err != nil || before != after {
				t.Fatalf("unchanged empty configuration queued work: before=%d after=%d err=%v", before, after, err)
			}
		})
	}
}

func TestGatewayActivityKeepsHistorySeparateFromCurrentState(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "gateway-history", NodeCapabilities{Gateway: true}, []networking.Candidate{{Address: "10.0.0.40", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.40", LANAddress: "10.0.0.40", EnabledKinds: []string{networking.KindLAN}})
	if _, err := store.db.Exec(`UPDATE gateway_states SET desired_revision=3,applied_revision=3,status='ready' WHERE gateway_node_id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []int64{2, 3} {
		if err := store.recordStandaloneTaskEvent(ctx, gatewayRouteTaskID(node.ID, revision), node.ID, "gateway.routes.apply", revision, "queued", "full gateway desired state queued"); err != nil {
			t.Fatal(err)
		}
	}
	for _, stopped := range []bool{false, true} {
		if stopped {
			if _, err := store.db.Exec(`UPDATE gateway_components SET desired_status='stopped' WHERE gateway_node_id=?`, node.ID); err != nil {
				t.Fatal(err)
			}
		}
		actions, err := store.ListActions(ctx, 100)
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, action := range actions {
			if action.Kind != "gateway.routes.apply" || action.Revision < 2 {
				continue
			}
			found++
			want := "superseded"
			if action.Revision == 3 {
				want = "ready"
				if stopped {
					want = "stopped"
				}
			}
			if action.Event != "queued" || action.CurrentState != want {
				encoded, _ := json.Marshal(action)
				t.Fatalf("history/current state mixed: %s want=%s", encoded, want)
			}
		}
		if found != 2 {
			t.Fatalf("missing gateway history: %d", found)
		}
	}
}
