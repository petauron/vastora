package center

import (
	"context"
	"encoding/json"
	"github.com/petauron/vastora/internal/landing"
	"testing"
)

func TestLandingEgressPreservesGrantsAndFencesOldExecutors(t *testing.T) {
	store, entry, egress := openMeridianLandingSourcesFixture(t)
	ctx := context.Background()
	for _, statement := range []string{
		`UPDATE meridian_cutover SET state='complete',subscription_authority='meridian' WHERE id=1`,
		`UPDATE agents SET capabilities_json=json_set(capabilities_json,'$.landingEgressIP',1)`,
	} {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`INSERT INTO settings(key,value) VALUES(?,json_object('revision',1,'nodeIds',json_array(?),'landingRegionCodes',json_object(?,'US'))) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, landingSelectionKey, egress, egress); err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err := store.db.QueryRow(`SELECT desired_json FROM landing_server_states WHERE node_id=?`, egress).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var state landing.ServerState
	if err := json.Unmarshal(before, &state); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agents SET capabilities_json=json_set(capabilities_json,'$.landingEgressIP',0) WHERE id=?`, entry); err != nil {
		t.Fatal(err)
	}
	change := LandingEgressInput{Revision: state.Revision, EgressIP: "2001:4860:4860::8888"}
	if err := store.SetLandingEgress(ctx, egress, change); err == nil {
		t.Fatal("accepted old entry executor")
	}
	if _, err := store.db.Exec(`UPDATE agents SET capabilities_json=json_set(capabilities_json,'$.landingEgressIP',1) WHERE id=?`, entry); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLandingEgress(ctx, egress, change); err != nil {
		t.Fatal(err)
	}
	var after []byte
	if err := store.db.QueryRow(`SELECT desired_json FROM landing_server_states WHERE node_id=?`, egress).Scan(&after); err != nil {
		t.Fatal(err)
	}
	var updated landing.ServerState
	if err := json.Unmarshal(after, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Plan.EgressIP != change.EgressIP || updated.Revision != state.Revision+1 || updated.Plan.Address != state.Plan.Address || len(updated.Plan.Sources) != len(state.Plan.Sources) {
		t.Fatalf("unexpected plan %+v", updated)
	}
	if err := store.SetLandingEgress(ctx, egress, change); err == nil {
		t.Fatal("accepted stale/pending update")
	}
	var grants int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM meridian_route_grants WHERE egress_node_id=?`, egress).Scan(&grants); err != nil || grants != 1 {
		t.Fatalf("grants changed %d %v", grants, err)
	}
}
