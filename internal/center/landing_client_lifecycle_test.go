package center

import (
	"context"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
)

func TestLandingChildCannotUseOrdinaryClientOperations(t *testing.T) {
	child := landing.FixedUser("child-a")
	for _, action := range []string{"create", "update", "set_enabled", "delete", "reset_traffic", "reveal_link", "reveal_subscription"} {
		t.Run(action, func(t *testing.T) {
			input := ThreeXUIClientCommandInput{ApplicationID: "app", Action: action, Email: child, NewEmail: child, InboundID: 1, InboundIDs: []int{1}}
			if _, err := normalizeThreeXUIClientCommandInput(input); err == nil {
				t.Fatal("ordinary API accepted a managed child identity")
			}
			if action == "update" {
				input.Email, input.NewEmail = "Phone", child
				if _, err := normalizeThreeXUIClientCommandInput(input); err == nil {
					t.Fatal("ordinary API can rename a parent into a managed child")
				}
			}
		})
	}
	if _, err := normalizeThreeXUIClientCommandInput(ThreeXUIClientCommandInput{ApplicationID: "app", Action: "create", NewEmail: "Phone", InboundIDs: []int{1}}); err != nil {
		t.Fatal("ordinary client creation changed", err)
	}
}

func TestLandingParentFenceRequiresEveryEntryAndNonemptyEvidence(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "controller", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.80", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}})
	now := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,status,runtime,role,created_at,updated_at) VALUES('parent-controller','Controller',?,?,'vastora-official/3x-ui','running','docker','master',?,?)`, node.ID, testSiteID(t, store), now, now); err != nil {
		t.Fatal(err)
	}
	parent := landing.Identity("parent-uuid")
	if _, err := store.db.Exec(`INSERT INTO three_x_ui_client_accounts(id,controller_id,email,metadata_json,pending_command_id,observed_at) VALUES(?,'parent-controller','Phone','{}','parent-command',?)`, parent, now); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ready, err := store.landingParentMutationReady(ctx, tx, "parent-command", parent)
	if err != nil || ready {
		t.Fatalf("no fence must not count as completed disconnection: %v %v", ready, err)
	}
	if _, err := tx.Exec(`INSERT INTO landing_client_blocks(parent_id,application_id,inbound_tag,user_name,identity) VALUES(?,'parent-controller','business','Phone',?)`, parent, parent); err != nil {
		t.Fatal(err)
	}
	ready, err = store.landingParentMutationReady(ctx, tx, "parent-command", parent)
	if err != nil || ready {
		t.Fatalf("missing route application counted as completed: %v %v", ready, err)
	}
	if _, err := tx.Exec(`INSERT INTO landing_proxy_states(node_id,application_id,landing_node_id,server_revision,source_address,desired_revision,applied_revision,desired_json,status,updated_at) VALUES(?,'parent-controller',?,1,'100.64.0.1',2,1,'{}','ready',?)`, node.ID, node.ID, now); err != nil {
		t.Fatal(err)
	}
	ready, err = store.landingParentMutationReady(ctx, tx, "parent-command", parent)
	if err != nil || ready {
		t.Fatalf("old revision counted as completed: %v %v", ready, err)
	}
	if _, err := tx.Exec(`UPDATE landing_proxy_states SET applied_revision=2 WHERE node_id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	ready, err = store.landingParentMutationReady(ctx, tx, "parent-command", parent)
	if err != nil || !ready {
		t.Fatalf("matching applied fence not recognized: %v %v", ready, err)
	}
	if _, err := store.landingParentMutationReady(ctx, tx, "another-command", parent); err == nil {
		t.Fatal("accepted another operation's fence")
	}
}
