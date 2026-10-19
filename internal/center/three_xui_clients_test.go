package center

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestHistoricalThreeXUIClientLinksRemainOneTimeAndMetadataSafe(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "archived-clients", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.80", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}})
	installation := seedLegacyDeployment(t, store, node, "10.0.0.80", "fixture-token", threeXUIRoleMaster)
	metadata := []ThreeXUIClientView{{Email: "MacBook", Enabled: true, TotalBytes: 10 << 30, UsedBytes: 1024, InboundIDs: []int{9}, HasSubscription: true}}
	inbounds := []ThreeXUIClientInbound{{ID: 9}}
	for _, item := range []struct{ action, secret, kind string }{
		{"list", "", ""}, {"list_inbounds", "", ""},
		{"reveal_link", "vless://11111111-2222-4333-8444-555555555555@entry.example.test:443", "client_link"},
		{"reveal_subscription", "https://subscription.example.test/sub/frozen-token", "subscription"},
	} {
		t.Run(item.action, func(t *testing.T) {
			commandID := "historical-client-" + item.action
			input := ThreeXUIClientCommandTask{Action: item.action, Email: "MacBook", InboundID: 9, SubscriptionBaseURI: "https://subscription.example.test/sub/"}
			safe := ThreeXUIClientCommandResult{Inbounds: inbounds, InboundsObserved: true}
			if item.action != "list_inbounds" {
				safe.Clients = metadata
				safe.ClientsObserved = true
			}
			encodedInput, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			encodedResult, err := json.Marshal(safe)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var secretID any
			if item.secret != "" {
				value, err := store.putSecret(ctx, tx, []byte(item.secret), "application-command:"+commandID)
				if err != nil {
					t.Fatal(err)
				}
				secretID = value
			}
			stamp := store.now().UTC().Format(time.RFC3339Nano)
			if _, err := tx.Exec(`INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,result_json,result_secret_id,state,created_at,updated_at) VALUES(?,?,?,?,'3xui.clients.manage',?,?,?,'succeeded',?,?)`, commandID, installation.ApplicationID, node.ID, node.ID, encodedInput, encodedResult, secretID, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			view, err := store.ApplicationCommand(ctx, commandID)
			if err != nil || view.State != "succeeded" || !view.InboundsObserved || !view.SubscriptionAvailable {
				t.Fatalf("historical result unreadable: %v", err)
			}
			if item.action == "list_inbounds" {
				if view.ClientsObserved || len(view.Clients) != 0 {
					t.Fatal("inbound history invented clients")
				}
			} else if !view.ClientsObserved || len(view.Clients) != 1 || view.Clients[0].UsedBytes != 1024 {
				t.Fatal("client metadata lost")
			}
			var public string
			if err := store.db.QueryRow(`SELECT CAST(result_json AS TEXT) FROM application_commands WHERE id=?`, commandID).Scan(&public); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(public, "vless://") || strings.Contains(public, "11111111-2222-4333-8444-555555555555") || strings.Contains(public, "frozen-token") {
				t.Fatal("private result leaked into public metadata")
			}
			if item.secret == "" {
				if view.ResultAvailable {
					t.Fatal("metadata acquired a secret")
				}
				return
			}
			const owner = "test-administrator"
			operation := "delivery-" + item.action
			value, err := store.RevealApplicationCommandResult(ctx, commandID, owner, operation)
			if err != nil || value != item.secret {
				t.Fatalf("historical secret unavailable: %v", err)
			}
			value, err = store.RevealApplicationCommandResult(ctx, commandID, owner, operation)
			if err != nil || value != item.secret {
				t.Fatalf("response retry lost secret: %v", err)
			}
			if _, err := store.RevealApplicationCommandResult(ctx, commandID, owner, "different-operation"); err == nil {
				t.Fatal("another operation claimed historical secret")
			}
			if err := store.AcknowledgeApplicationCommandResult(ctx, commandID, owner, operation); err != nil {
				t.Fatal(err)
			}
			if _, err := store.RevealApplicationCommandResult(ctx, commandID, owner, operation); err == nil {
				t.Fatal("acknowledged secret still available")
			}
		})
	}
}

func TestHistoricalThreeXUIClientCommandRetainsMultipleSiteNodesWithoutDispatch(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "controller", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.80", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}})
	workerNode := enrollOrchestrationNode(t, store, "remote-worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.1.81", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.1.81", LANAddress: "10.0.1.81", EnabledKinds: []string{networking.KindLAN}})
	remoteSite, err := store.CreateSite(ctx, SiteInput{Name: "Remote", Code: "remote-clients", Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE agents SET site_id = ? WHERE id = ?`, remoteSite.ID, workerNode.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	siteID := testSiteID(t, store)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at) VALUES('three-x-ui-controller', '3x-ui', ?, ?, ?, '', 'running', 'docker', 'master', ?, ?)`, node.ID, siteID, threeXUIAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	selectTestThreeXUIController(t, store, "three-x-ui-controller")
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at)
		VALUES('three-x-ui-remote-worker', '3x-ui Remote', ?, ?, ?, '', 'running', 'docker', 'worker', ?, ?)`, workerNode.ID, remoteSite.ID, threeXUIAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO three_x_ui_nodes(worker_application_id, master_application_id, remote_node_id, status, created_at, updated_at)
		VALUES('three-x-ui-remote-worker', 'three-x-ui-controller', 10, 'ready', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, app_protocol, management, observed_listen, status, created_at, updated_at)
		VALUES('reality-9', 'three-x-ui-controller', ?, 'inbound-9', 'tcp', 30009, 30009, '10.0.0.80:30009', 'observed', 'vless/tcp/reality', 0, '10.0.0.80', 'ready', ?, ?),
		('reality-10', 'three-x-ui-remote-worker', ?, 'inbound-10', 'tcp', 30010, 30010, '10.0.1.81:30010', 'observed', 'vless/tcp/reality', 0, '10.0.1.81', 'ready', ?, ?)`, siteID, now, now, remoteSite.ID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO three_x_ui_inbound_plans(service_id, inbound_tag, total_bytes, reset_day, next_reset_at, revision, status, updated_at)
		VALUES('reality-9', 'vastora-node-9', 0, 0, '', 1, 'active', ?),
		('reality-10', 'vastora-node-10', 0, 0, '', 1, 'active', ?)`, now, now); err != nil {
		t.Fatal(err)
	}

	command, err := store.CreateThreeXUIClientCommand(ctx, ThreeXUIClientCommandInput{ApplicationID: "three-x-ui-controller", Action: "create", NewEmail: "Router", InboundIDs: []int{10, 9, 10}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err := store.db.QueryRow(`SELECT input_json FROM application_commands WHERE id=?`, command.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var archived ThreeXUIClientCommandTask
	if err := json.Unmarshal(before, &archived); err != nil {
		t.Fatal(err)
	}
	if len(archived.InboundIDs) != 2 || archived.InboundIDs[0] != 9 || archived.InboundIDs[1] != 10 || len(archived.Inbounds) != 2 || archived.Inbounds[1].ApplicationID != "three-x-ui-remote-worker" {
		t.Fatal("historical multi-site selection or identity changed")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.claimApplicationCommand(ctx, tx, node.ID)
	_ = tx.Rollback()
	if err != nil || task != nil {
		t.Fatalf("retired client task dispatched: %v %v", task, err)
	}
	var after []byte
	var state string
	var attempt int
	if err := store.db.QueryRow(`SELECT input_json,state,attempt FROM application_commands WHERE id=?`, command.ID).Scan(&after, &state, &attempt); err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || state != "pending" || attempt != 0 {
		t.Fatal("selection mutated the archived command or manufactured progress")
	}
	completed, err := store.ApplicationCommand(ctx, command.ID)
	if err != nil || completed.State != "pending" || completed.ResultAvailable || len(completed.Clients) != 0 {
		t.Fatalf("historical pending operation gained a result: %v", err)
	}
}
