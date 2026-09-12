package center

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
)

func TestLandingClientTaskPipelineAndOfflineRevocation(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	nodes := make([]AgentCredential, 2)
	for i, address := range []string{"10.0.0.80", "10.0.0.81"} {
		nodes[i] = enrollOrchestrationNode(t, store, address, NodeCapabilities{Docker: true}, []networking.Candidate{{Address: address, Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: address, LANAddress: address, EnabledKinds: []string{networking.KindLAN}})
	}
	entry, owner := nodes[0], nodes[1]
	exec(`UPDATE agents SET tailscale_ownership='managed' WHERE id IN (?,?)`, entry.ID, owner.ID)
	exec(`UPDATE agent_network_profiles SET headscale_address='100.64.0.8' WHERE agent_id=?`, entry.ID)
	exec(`UPDATE agent_network_profiles SET headscale_address='100.64.0.9' WHERE agent_id=?`, owner.ID)
	now := store.now().UTC().Format(time.RFC3339Nano)
	site := testSiteID(t, store)
	exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,status,runtime,role,created_at,updated_at) VALUES('client-controller','3x-ui',?,?,'vastora-official/3x-ui','running','docker','master',?,?)`, entry.ID, site, now, now)
	selectTestThreeXUIController(t, store, "client-controller")
	exec(`INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,app_protocol,status,created_at,updated_at) VALUES('client-inbound','client-controller',?,'inbound-9','tcp',30009,30009,'100.64.0.8:30009','observed','vless/tcp/reality','ready',?,?)`, site, now, now)
	exec(`INSERT INTO three_x_ui_inbound_plans(service_id,inbound_tag,updated_at) VALUES('client-inbound','business',?)`, now)
	exec(`INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,sni_hostname,dns_provider,tls_enabled,status,created_at,updated_at) VALUES('client-publication','client-inbound','public_shared_443','application_node',?,'entry.example.test','example.com','manual',0,'ready',?,?)`, entry.ID, now, now)
	peer := landing.PeerIdentity{ID: "entry", PublicKey: "entry-key", Address: "100.64.0.8"}
	peerJSON, _ := json.Marshal(peer)
	exec(`INSERT INTO landing_client_capabilities(node_id,generation,peer_json,observed_at) VALUES(?,?,?,?)`, entry.ID, landing.ClientRuntimeGeneration, peerJSON, now)
	parentUUID := "11111111-2222-4333-8444-555555555555"
	parent := landing.Identity(parentUUID)
	metadata, _ := json.Marshal(ThreeXUIClientView{ID: parent, Email: "Phone", Enabled: true, InboundIDs: []int{9}, HasSubscription: true})
	exec(`INSERT INTO three_x_ui_client_accounts(id,controller_id,email,metadata_json,mode,observed_at) VALUES(?,'client-controller','Phone',?,'both',?)`, parent, metadata, now)
	if err := store.SelectLanding(ctx, LandingSelection{NodeIDs: []string{owner.ID}}); err != nil {
		t.Fatal(err)
	}
	claimLanding := func(server bool) *AgentTask {
		t.Helper()
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var task *AgentTask
		if server {
			task, err = store.claimLandingServerTask(ctx, tx, owner.ID)
		} else {
			task, err = store.claimLandingProxyTask(ctx, tx, entry.ID)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return task
	}
	completeServer := func(task *AgentTask) {
		t.Helper()
		if task == nil {
			t.Fatal("missing server task")
		}
		peer := &landing.PeerIdentity{ID: "landing", PublicKey: "landing-key", Address: "100.64.0.9"}
		if err := store.completeLandingServer(ctx, owner.ID, task.Revision, task.Attempt, true, peer); err != nil {
			t.Fatal(err)
		}
	}
	completeServer(claimLanding(true))
	input := LandingClientGrantInput{ParentID: parent, ServiceID: "client-inbound", LandingNodeID: owner.ID, Mode: landing.BothMode, Enabled: true, ConfirmSessionReset: true}
	grant, err := store.ConfigureClientLanding(ctx, input)
	if err != nil || grant.Status != "preparing" || grant.AppliedRevision != 0 {
		t.Fatalf("grant queued: %+v %v", grant, err)
	}
	completeController := func(phase string) landing.ControllerTask {
		t.Helper()
		task := claimTask(t, store, entry)
		if task.ClientCommand == nil || task.ClientCommand.Landing == nil || task.ClientCommand.Landing.Phase != phase || !strings.HasPrefix(task.ID, "application-command-") {
			t.Fatalf("missing %s controller task", phase)
		}
		plan := *task.ClientCommand.Landing
		result := landing.ControllerResult{GrantID: plan.Grant.ID, Revision: plan.Revision, Phase: phase}
		if phase == "activate" {
			query := "@entry.example.test:443?security=reality&type=tcp&flow=xtls-rprx-vision&sni=example.com&pbk=key&sid=deadbeef"
			result.BaseLink, result.FixedLink, result.SubscriptionToken = "vless://"+parentUUID+query, "vless://"+plan.FixedUUID+query, "parent-secret-token"
		}
		raw, _ := json.Marshal(ApplicationTaskResult{ClientCommand: &ThreeXUIClientCommandResult{Landing: &result}})
		if err := store.CompleteTask(ctx, entry.ID, entry.Credential, task.ID, task.Attempt, true, "", raw, task.RequiredRuntimeGeneration); err != nil {
			t.Fatal(err)
		}
		var public string
		if err := store.db.QueryRow(`SELECT CAST(result_json AS TEXT) FROM application_commands WHERE id=?`, task.ID).Scan(&public); err != nil || strings.Contains(public, "vless://") || strings.Contains(public, "parent-secret-token") || strings.Contains(public, plan.FixedUUID) {
			t.Fatal("controller command exposed private material", err)
		}
		return plan
	}
	prepared := completeController("prepare")
	if prepared.FixedUUID == parentUUID || landing.Identity(prepared.FixedUUID) != prepared.Grant.FixedIdentity {
		t.Fatal("child did not receive an independent identity")
	}
	if task := claimLanding(false); task != nil {
		t.Fatal("entry routes became available before source authorization")
	}
	server := claimLanding(true)
	if server == nil || len(server.LandingServerState.Plan.Sources) != 1 || !server.LandingServerState.Plan.Sources[0].TCPOnly || server.LandingServerState.Plan.Sources[0].Address != "100.64.0.8" {
		t.Fatal("grant did not authorize the exact TCP source")
	}
	completeServer(server)
	routes := claimLanding(false)
	if routes == nil || routes.LandingProxyState.Clients == nil || len(routes.LandingProxyState.Clients.Grants) != 1 || !routes.LandingProxyState.Clients.Grants[0].Enabled {
		t.Fatal("missing scoped entry routes")
	}
	if err := store.completeLandingProxy(ctx, entry.ID, routes.Revision, routes.Attempt, true); err != nil {
		t.Fatal(err)
	}
	completeController("activate")
	grants, err := store.LandingClientGrants(ctx, parent)
	if err != nil || len(grants) != 1 || grants[0].Status != "ready" || grants[0].AppliedRevision != grant.Revision {
		t.Fatal("grant became ready without the full task sequence", err)
	}
	for _, enabled := range []bool{false, true} {
		command, err := store.CreateThreeXUIClientCommand(ctx, ThreeXUIClientCommandInput{ApplicationID: "client-controller", Action: "set_enabled", Email: "Phone", Enabled: enabled, ConfirmSessionReset: true})
		if err != nil {
			t.Fatal("queue shared parent change", err)
		}
		// The pending parent command must yield to its entry fence instead of
		// applying native account changes while existing streams still run.
		fence := claimTask(t, store, entry)
		if fence.Kind != "landing.proxy.apply" || fence.LandingProxyState == nil || len(fence.LandingProxyState.Clients.BlockedUsers) != 2 {
			t.Fatal("parent update bypassed the base/child disconnection fence")
		}
		if err := store.completeLandingProxy(ctx, entry.ID, fence.Revision, fence.Attempt, true); err != nil {
			t.Fatal(err)
		}
		parentTask := claimTask(t, store, entry)
		if parentTask.ID != command.ID || parentTask.ClientCommand == nil || parentTask.ClientCommand.ManagedParentID != parent {
			t.Fatal("parent mutation lost its stable identity")
		}
		result, _ := json.Marshal(ApplicationTaskResult{ClientCommand: &ThreeXUIClientCommandResult{ClientsObserved: true, Inbounds: parentTask.ClientCommand.Inbounds, Clients: []ThreeXUIClientView{{ID: parent, Email: "Phone", Enabled: enabled, InboundIDs: []int{9}, HasSubscription: true, HasLanding: true}}}})
		if err := store.CompleteTask(ctx, entry.ID, entry.Credential, parentTask.ID, parentTask.Attempt, true, "", result, parentTask.RequiredRuntimeGeneration); err != nil {
			t.Fatal(err)
		}
		completed, err := store.ApplicationCommand(ctx, command.ID)
		if err != nil || completed.State != "succeeded" {
			t.Fatalf("parent command not confirmed: state=%s error=%s err=%v", completed.State, completed.Error, err)
		}
		if enabled {
			completeController("prepare")
		}
		routes = claimLanding(false)
		if routes == nil || routes.LandingProxyState.Clients.Grants[0].Enabled != enabled {
			var status string
			if err := store.db.QueryRow(`SELECT status FROM landing_proxy_states WHERE node_id=?`, entry.ID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			t.Fatalf("parent lifecycle did not update its child's route authority: enabled=%v task=%+v status=%s", enabled, routes, status)
		}
		if err := store.completeLandingProxy(ctx, entry.ID, routes.Revision, routes.Attempt, true); err != nil {
			t.Fatal(err)
		}
		if enabled {
			completeController("activate")
		}
		grants, err = store.LandingClientGrants(ctx, parent)
		if err != nil || len(grants) != 1 || enabled && grants[0].Status != "ready" || !enabled && grants[0].Status != "paused" {
			t.Fatal("shared parent status was not reconciled", err)
		}
	}
	// The old landing can be offline: entry deny and controller retirement
	// must not wait for a successful connection to that landing.
	exec(`UPDATE landing_server_states SET status='failed' WHERE node_id=?`, owner.ID)
	input.Enabled, input.Revision = false, grants[0].Revision
	if _, err := store.ConfigureClientLanding(ctx, input); err != nil {
		t.Fatal(err)
	}
	routes = claimLanding(false)
	if routes == nil || routes.LandingProxyState.Clients.Grants[0].Enabled {
		t.Fatal("revocation required the offline landing or left its route enabled")
	}
	if err := store.completeLandingProxy(ctx, entry.ID, routes.Revision, routes.Attempt, true); err != nil {
		t.Fatal(err)
	}
	completeController("retire")
	grants, err = store.LandingClientGrants(ctx, parent)
	if err != nil || len(grants) != 0 {
		t.Fatal("retirement retained active topology references", err)
	}
	routes = claimLanding(false)
	if routes == nil || len(routes.LandingProxyState.Clients.Grants) != 0 {
		t.Fatal("retired child route was not cleaned up")
	}
	if err := store.completeLandingProxy(ctx, entry.ID, routes.Revision, routes.Attempt, true); err != nil {
		t.Fatal(err)
	}
	server = claimLanding(true)
	if server == nil || len(server.LandingServerState.Plan.Sources) != 0 {
		t.Fatal("last retired grant retained its network authorization")
	}
	// A confirmed parent deletion removes old FK/secret references and queues
	// restoration. The landing is offline and its server row may be removed.
	completeServer(server)
	command, err := store.CreateThreeXUIClientCommand(ctx, ThreeXUIClientCommandInput{ApplicationID: "client-controller", Action: "delete", Email: "Phone", ConfirmSessionReset: true})
	if err != nil {
		t.Fatal(err)
	}
	fence := claimTask(t, store, entry)
	if fence.Kind != "landing.proxy.apply" {
		t.Fatal("deletion skipped disconnection")
	}
	if err := store.completeLandingProxy(ctx, entry.ID, fence.Revision, fence.Attempt, true); err != nil {
		t.Fatal(err)
	}
	deletion := claimTask(t, store, entry)
	if deletion.ID != command.ID {
		t.Fatal("missing parent deletion task")
	}
	deletedResult, _ := json.Marshal(ApplicationTaskResult{ClientCommand: &ThreeXUIClientCommandResult{ClientsObserved: true, Inbounds: deletion.ClientCommand.Inbounds, Clients: []ThreeXUIClientView{}}})
	if err := store.CompleteTask(ctx, entry.ID, entry.Credential, deletion.ID, deletion.Attempt, true, "", deletedResult, deletion.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"landing_client_grants", "landing_client_blocks", "three_x_ui_client_accounts"} {
		var count int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("confirmed deletion retained %s: %d %v", table, count, err)
		}
	}
	exec(`DELETE FROM landing_server_states WHERE node_id=?`, owner.ID)
	routes = claimLanding(false)
	if routes == nil || routes.LandingProxyState.Active() {
		t.Fatal("final restoration requires a deleted landing server")
	}
	if err := store.completeLandingProxy(ctx, entry.ID, routes.Revision, routes.Attempt, true); err != nil {
		t.Fatal(err)
	}
}
