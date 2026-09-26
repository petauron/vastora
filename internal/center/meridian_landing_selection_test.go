package center

import (
	"context"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestMeridianLandingCanRestoreDrainingNodeWithoutLegacyController(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "restored-exit", NodeCapabilities{Docker: true},
		[]networking.Candidate{{Address: "100.64.0.82", Interface: "tailscale0", Kind: networking.KindHeadscale}},
		networking.Profile{ServiceAddress: "100.64.0.82", HeadscaleAddress: "100.64.0.82", EnabledKinds: []string{networking.KindHeadscale}})
	if _, err := store.db.Exec(`UPDATE agents SET tailscale_ownership='managed',last_seen_at=? WHERE id=?`, store.now().UTC().Format(time.RFC3339Nano), node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE meridian_cutover SET state='not_required',subscription_authority='meridian' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	selected := LandingSelection{NodeIDs: []string{node.ID}, LandingRegionCodes: map[string]string{node.ID: "US"}}
	if err := store.SelectMeridianLanding(ctx, selected); err != nil {
		t.Fatal(err)
	}
	if err := store.SelectMeridianLanding(ctx, LandingSelection{Revision: 1}); err != nil {
		t.Fatal(err)
	}
	selected.Revision = 2
	if err := store.SelectMeridianLanding(ctx, selected); err != nil {
		t.Fatal(err)
	}
	view, err := store.Landing(ctx)
	if err != nil || len(view.NodeIDs) != 1 || len(view.RetiringNodeIDs) != 0 || view.Revision != 3 || len(view.Servers) != 1 || view.Servers[0].Status != "pending" {
		t.Fatalf("restored view=%+v err=%v", view, err)
	}
	var grants int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM meridian_route_grants`).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("pool selection created account authorization: %d %v", grants, err)
	}
	if err := store.SelectMeridianLanding(ctx, selected); err == nil {
		t.Fatal("stale restoration accepted")
	}
}

func TestMeridianLandingCannotRemoveAuthorizedExit(t *testing.T) {
	store, _, egress := openMeridianLandingSourcesFixture(t)
	ctx := context.Background()
	if _, err := store.db.Exec(`INSERT INTO settings(key,value) VALUES(?,json_object('revision',1,'nodeIds',json_array(?),'landingRegionCodes',json_object(?,'US'))) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, landingSelectionKey, egress, egress); err != nil {
		t.Fatal(err)
	}
	if err := store.SelectMeridianLanding(ctx, LandingSelection{Revision: 1}); err == nil {
		t.Fatal("authorized exit was removed")
	}
	selection, err := store.LandingSelection(ctx)
	if err != nil || len(selection.NodeIDs) != 1 || selection.NodeIDs[0] != egress {
		t.Fatalf("rejected removal changed selection: %+v %v", selection, err)
	}
}
