package center

import (
	"context"
	"testing"

	"github.com/petauron/vastora/internal/networking"
)

func TestDeleteAgentRequiresDisabledAndRemovesRecord(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "retired", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.80", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}})
	if err := store.DeleteAgent(ctx, node.ID); err == nil {
		t.Fatal("active node deleted")
	}
	if err := store.DisableAgent(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAgent(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	agents, err := store.ListAgents(ctx)
	if err != nil || len(agents) != 0 {
		t.Fatalf("node was not removed: %v, %v", agents, err)
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{Version: "test"}); err == nil {
		t.Fatal("deleted credential accepted")
	}
	if err := store.DeleteAgent(ctx, node.ID); err == nil {
		t.Fatal("missing node accepted")
	}
}

func TestDeleteAgentProtectsLandingSelection(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "landing", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.80", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}})
	if err := store.DisableAgent(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO settings(key,value) VALUES(?,json_object('nodeId',?,'revision',1)) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, landingSelectionKey, node.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAgent(ctx, node.ID); err == nil {
		t.Fatal("selected landing node deleted")
	}
}

func TestDeleteAgentCleansStoppedApplicationsButProtectsActiveOnes(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "retired", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.80", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}})
	if err := store.DisableAgent(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,status,created_at,updated_at) SELECT 'retired-app','app',id,site_id,'test/app','running','','' FROM agents WHERE id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAgent(ctx, node.ID); err == nil {
		t.Fatal("node with active application deleted")
	}
	if _, err := store.db.Exec(`UPDATE applications SET status='stopped' WHERE id='retired-app'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER refuse_test_node_delete BEFORE DELETE ON agents BEGIN SELECT RAISE(ABORT, 'dependency'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAgent(ctx, node.ID); err == nil {
		t.Fatal("delete constraint ignored")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM applications WHERE id='retired-app'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed deletion removed application history: %d, %v", count, err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER refuse_test_node_delete`); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAgent(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
}
