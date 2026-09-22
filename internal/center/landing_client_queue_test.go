package center

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
)

func TestLandingClientBacklogPreservesOneNativeWriter(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "controller", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.80", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}})
	landingNode := enrollOrchestrationNode(t, store, "landing", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.81", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.81", LANAddress: "10.0.0.81", EnabledKinds: []string{networking.KindLAN}})
	now := store.now().UTC().Format(time.RFC3339Nano)
	site := testSiteID(t, store)
	if _, err := store.db.Exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,status,runtime,role,created_at,updated_at) VALUES('queue-controller','3x-ui',?,?,'vastora-official/3x-ui','running','docker','master',?,?)`, node.ID, site, now, now); err != nil {
		t.Fatal(err)
	}
	selectTestThreeXUIController(t, store, "queue-controller")
	if _, err := store.db.Exec(`INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,app_protocol,status,created_at,updated_at) VALUES('queue-inbound','queue-controller',?,'inbound-9','tcp',30009,30009,'100.64.0.8:30009','observed','vless/tcp/reality','ready',?,?)`, site, now, now); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := range 3 {
		id := fmt.Sprintf("grant-%d", i)
		parent := landing.Identity(fmt.Sprintf("parent-%d", i))
		credential := fmt.Sprintf("aaaaaaaa-bbbb-4ccc-8ddd-%012d", i)
		grant := landing.ClientGrant{ID: id, ParentID: parent, BaseIdentity: parent, BaseUser: fmt.Sprintf("Parent-%d", i), InboundTag: "business", FixedUser: landing.FixedUser(id), FixedIdentity: landing.Identity(credential), Peer: landing.PeerIdentity{ID: "landing", PublicKey: "landing-key", Address: "100.64.0.9"}, Mode: landing.FixedMode, Enabled: true}
		if _, err := tx.Exec(`INSERT INTO three_x_ui_client_accounts(id,controller_id,email,metadata_json,observed_at) VALUES(?,'queue-controller',?,'{}',?)`, parent, grant.BaseUser, now); err != nil {
			t.Fatal(err)
		}
		secretID, err := store.putSecret(ctx, tx, []byte(credential), "landing-credential:"+id)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(grant)
		source, _ := json.Marshal(landing.PeerIdentity{ID: "entry", PublicKey: "entry-key", Address: "100.64.0.8"})
		if _, err := tx.Exec(`INSERT INTO landing_client_grants(id,parent_id,application_id,service_id,landing_node_id,source_peer_json,grant_json,credential_secret_id,status,updated_at) VALUES(?,?,'queue-controller','queue-inbound',?,?,?,?,'preparing',?)`, id, parent, landingNode.ID, source, data, secretID, now); err != nil {
			t.Fatal(err)
		}
		record, err := readLandingGrant(ctx, tx, id)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if _, err := tx.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,expires_at,created_at,updated_at) VALUES('landing-fence',?,'landing-fence-task','landing.server.apply',1,'landing-fence-session','landing-fence-digest',X'00','unknown','apply',?,?,?)`, landingNode.ID, now, now, now); err != nil {
				t.Fatal(err)
			}
			if err := store.queueLandingClientCommand(ctx, tx, record, "prepare"); err != nil {
				t.Fatal(err)
			}
			var queued int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM application_commands`).Scan(&queued); err != nil || queued != 0 {
				t.Fatalf("controller work bypassed the landing execution fence: queued=%d err=%v", queued, err)
			}
			if _, err := tx.Exec(`DELETE FROM task_executions WHERE id='landing-fence'`); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.queueLandingClientCommand(ctx, tx, record, "prepare"); err != nil {
			t.Fatal("multiple grants must form a durable backlog, not violate the native writer constraint", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := store.queueNextLandingClientCommand(ctx, tx, node.ID); err != nil {
			t.Fatal(err)
		}
		var active int
		var commandID, grantID string
		if err := tx.QueryRow(`SELECT COUNT(*) FROM application_commands WHERE state='pending'`).Scan(&active); err != nil || active != 1 {
			t.Fatalf("expected exactly one active native writer: %d %v", active, err)
		}
		if err := tx.QueryRow(`SELECT id,json_extract(input_json,'$.grantId') FROM application_commands WHERE state='pending'`).Scan(&commandID, &grantID); err != nil || grantID != fmt.Sprintf("grant-%d", i) {
			t.Fatal("durable grant queue skipped or duplicated work", err)
		}
		if _, err := tx.Exec(`UPDATE application_commands SET state='succeeded' WHERE id=?`, commandID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE landing_client_grants SET status='prepared' WHERE id=?`, grantID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
}
