package center

import (
	"context"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
)

func TestLandingAccountNameReuseDoesNotTransferAuthority(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "controller", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.80", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}})
	now := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,status,runtime,role,created_at,updated_at) VALUES('identity-controller','Controller',?,?,'vastora-official/3x-ui','running','docker','master',?,?)`, node.ID, testSiteID(t, store), now, now); err != nil {
		t.Fatal(err)
	}
	old, next := landing.Identity("old-uuid"), landing.Identity("new-uuid")
	if _, err := store.db.Exec(`INSERT INTO three_x_ui_client_accounts(id,controller_id,email,metadata_json,mode,observed_at) VALUES(?,'identity-controller','Phone','{}','both',?)`, old, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) VALUES('inventory','identity-controller',?,?,'3xui.clients.manage','{}','succeeded',?,?)`, node.ID, node.ID, now, now); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result := ThreeXUIClientCommandResult{ClientsObserved: true, Clients: []ThreeXUIClientView{{ID: next, Email: "Phone"}, {ID: landing.Identity("unrelated"), Email: "Laptop"}}}
	if err := store.observeLandingAccounts(ctx, tx, "inventory", &result); err != nil {
		t.Fatal("one replacement blocked the complete inventory", err)
	}
	var mode, oldName string
	if err := tx.QueryRow(`SELECT mode FROM three_x_ui_client_accounts WHERE id=?`, next).Scan(&mode); err != nil || mode != "fixed" {
		t.Fatal("replacement inherited publishing authority", err)
	}
	if err := tx.QueryRow(`SELECT email FROM three_x_ui_client_accounts WHERE id=?`, old).Scan(&oldName); err != nil || validThreeXUIClientName(oldName) {
		t.Fatal("retired parent became an ordinary mutable client", err)
	}
	if err := requireLandingControllerTransferSafe(ctx, tx, "identity-controller", "target"); err != nil {
		t.Fatal("plain client inventory blocked migration", err)
	}
	if _, err := tx.Exec(`UPDATE three_x_ui_client_accounts SET managed_quota=1 WHERE id=?`, old); err != nil {
		t.Fatal(err)
	}
	if err := requireLandingControllerTransferSafe(ctx, tx, "identity-controller", "target"); err == nil {
		t.Fatal("migration discarded a journal after grants were revoked")
	}
	if err := requireLandingControllerTransferSafe(ctx, tx, "unrelated", "target"); err != nil {
		t.Fatal("unrelated controller was blocked", err)
	}
}
