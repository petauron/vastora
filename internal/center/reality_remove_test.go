package center

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
)

func localRealityRemovalFixture(t *testing.T) (*Store, AgentCredential) {
	t.Helper()
	store := openOrchestrationStore(t)
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "controller", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.90", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.90", LANAddress: "10.0.0.90", EnabledKinds: []string{networking.KindLAN}})
	now, site := store.now().UTC().Format(time.RFC3339Nano), testSiteID(t, store)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,status,runtime,role,created_at,updated_at) VALUES('controller','3x-ui',?,?,'vastora-official/3x-ui','running','docker','master',?,?)`, node.ID, site, now, now)
	selectTestThreeXUIController(t, store, "controller")
	for _, service := range []struct {
		id, name, protocol, appProtocol, source string
		management                              bool
	}{
		{"local", "inbound-9", "tcp", "vless/tcp/reality", "observed", false},
		{"panel", "panel", "http", "", "catalog", true},
		{"subscription", "subscription", "http", "", "catalog", false},
	} {
		exec(`INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,app_protocol,management,status,created_at,updated_at) VALUES(?,'controller',?,?,?,443,443,'10.0.0.90:443',?,?,?,'ready',?,?)`, service.id, site, service.name, service.protocol, service.source, service.appProtocol, service.management, now, now)
	}
	exec(`INSERT INTO three_x_ui_inbound_plans(service_id,inbound_tag,updated_at) VALUES('local','local-node',?)`, now)
	for _, id := range []string{"local", "panel", "subscription"} {
		exec(`INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,sni_hostname,dns_provider,desired_revision,applied_revision,status,created_at,updated_at) VALUES(?,?,'public_shared_443','application_node',?,?,?,'manual',1,1,'ready',?,?)`, id+"-entry", id, node.ID, id+".example.test", id+".example.test", now, now)
	}
	// Panel/subscription actually use their independent managed tunnel paths.
	exec(`UPDATE publications SET kind='cloudflare_tunnel', ingress_owner='tunnel_connector' WHERE service_id IN ('panel','subscription')`)
	return store, node
}

func claimLocalRemoval(t *testing.T, store *Store, nodeID string) *AgentTask {
	t.Helper()
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	task, err := store.claimApplicationCommand(context.Background(), tx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestLocalRealityRemovalRetainsControllerAndFencesStaleHeartbeat(t *testing.T) {
	store, node := localRealityRemovalFixture(t)
	ctx := context.Background()
	for _, id := range []string{"panel", "subscription", "unknown"} {
		if _, err := store.CreateRealityRemoveCommand(ctx, RealityRemoveCommandInput{ServiceID: id}); err == nil {
			t.Fatalf("accepted unrelated service %s", id)
		}
	}
	command, err := store.CreateRealityRemoveCommand(ctx, RealityRemoveCommandInput{ServiceID: "local"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.CreateRealityRemoveCommand(ctx, RealityRemoveCommandInput{ServiceID: "local"})
	if err != nil || again.ID != command.ID {
		t.Fatalf("retry did not reuse pending command: %+v %v", again, err)
	}
	task := claimLocalRemoval(t, store, node.ID)
	if task == nil || task.ApplicationCommand == nil || task.ApplicationCommand.InboundID != 9 || task.ApplicationCommand.InboundTag != "local-node" || task.ApplicationCommand.TargetNodeID != 0 {
		t.Fatalf("unsafe command: %+v", task)
	}
	heartbeat := func(observations []ApplicationEndpointObservation) {
		t.Helper()
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var cleanups []publicationCleanup
		if err := store.reconcileApplicationEndpoints(ctx, tx, node.ID, observations, store.now(), &cleanups); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	heartbeat(nil)
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM services WHERE id='local'`).Scan(&status); err != nil || status != "ready" {
		t.Fatalf("lost removal retry entry: %s %v", status, err)
	}
	raw, _ := json.Marshal(ApplicationTaskResult{ApplicationCommand: &RealityCommandResult{Action: "remove", InboundID: 9, InboundTag: "local-node"}})
	if err := store.completeApplicationCommand(ctx, node.ID, task.ID, task.Attempt, true, "", raw, false); err != nil {
		t.Fatal(err)
	}
	if err := store.completeApplicationCommand(ctx, node.ID, task.ID, task.Attempt, true, "", raw, false); err != nil {
		t.Fatal(err)
	}
	stale := []ApplicationEndpointObservation{{AppKey: threeXUIAppKey, Name: "inbound-9", Protocol: "tcp", AppProtocol: "vless/tcp/reality", Listen: "10.0.0.90", Port: 443, Enabled: true, InboundTag: "local-node"}}
	heartbeat(stale)
	for _, check := range []struct{ query, want string }{
		{`SELECT status FROM applications WHERE id='controller'`, "running"},
		{`SELECT status FROM services WHERE id='local'`, "stopped"},
		{`SELECT status FROM publications WHERE id='local-entry'`, "stopped"},
		{`SELECT status FROM services WHERE id='panel'`, "ready"},
		{`SELECT status FROM services WHERE id='subscription'`, "ready"},
		{`SELECT status FROM publications WHERE id='panel-entry'`, "ready"},
		{`SELECT status FROM publications WHERE id='subscription-entry'`, "ready"},
		{`SELECT controller_application_id FROM three_x_ui_control_plane WHERE id=1`, "controller"},
	} {
		if err := store.db.QueryRowContext(ctx, check.query).Scan(&status); err != nil || status != check.want {
			t.Fatalf("%s: got=%s want=%s err=%v", check.query, status, check.want, err)
		}
	}
	var encoded []byte
	if err := store.db.QueryRowContext(ctx, `SELECT desired_json FROM node_listener_states WHERE node_id=?`, node.ID).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var listener gateway.NodeListenerState
	if json.Unmarshal(encoded, &listener) != nil || len(listener.Listener.Routes) != 0 {
		t.Fatal("local listener route was retained")
	}
	// Reusing a numeric inbound id with a new tag must remain possible.
	stale[0].InboundTag = "new-local-node"
	heartbeat(stale)
	if err := store.db.QueryRowContext(ctx, `SELECT inbound_tag FROM three_x_ui_inbound_plans WHERE service_id='local'`).Scan(&status); err != nil || status != "new-local-node" {
		t.Fatalf("new inbound was fenced: %s %v", status, err)
	}
}

func TestLocalRealityRemovalWaitsForLandingRestoration(t *testing.T) {
	store, node := localRealityRemovalFixture(t)
	ctx := context.Background()
	state := landing.DesiredState{NodeID: node.ID, Revision: 1, Proxy: &landing.ProxyPlan{ApplicationID: "controller", InboundTags: []string{"local-node"}, Peer: landing.PeerIdentity{ID: "landing", PublicKey: "key", Address: "100.64.0.8"}}}
	encoded, _ := json.Marshal(state)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO landing_proxy_states(node_id,application_id,landing_node_id,source_address,desired_revision,applied_revision,desired_json,status,updated_at) VALUES(?,'controller',?,'100.64.0.9',1,1,?,'ready',?)`, node.ID, node.ID, encoded, store.now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRealityRemoveCommand(ctx, RealityRemoveCommandInput{ServiceID: "local"}); err != nil {
		t.Fatal(err)
	}
	if task := claimLocalRemoval(t, store, node.ID); task != nil {
		t.Fatal("removal ran before landing restoration")
	}
	if err := store.ConfigureLandingProxy(ctx, "controller", LandingProxyInput{Enabled: true, LandingNodeID: node.ID, Revision: 2}); err == nil {
		t.Fatal("allowed landing enable during removal")
	}
	var revision int
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision,desired_json FROM landing_proxy_states WHERE node_id=?`, node.ID).Scan(&revision, &encoded); err != nil {
		t.Fatal(err)
	}
	var restored landing.DesiredState
	if json.Unmarshal(encoded, &restored) != nil || revision != 2 || restored.Proxy != nil {
		t.Fatal("landing restore was not queued")
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE landing_proxy_states SET status='stopped',applied_revision=desired_revision WHERE node_id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	if task := claimLocalRemoval(t, store, node.ID); task == nil {
		t.Fatal("restored node removal did not proceed")
	}
}
