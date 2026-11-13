package center

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
)

func TestRetiredLandingClientPipelineRetainsGrantWithoutExecuting(t *testing.T) {
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
	exec(`INSERT INTO agent_private_peer_capabilities(node_id,generation,peer_json,observed_at) VALUES(?,?,?,?)`, entry.ID, landing.ClientRuntimeGeneration, peerJSON, now)
	parentUUID := "11111111-2222-4333-8444-555555555555"
	parent := landing.Identity(parentUUID)
	metadata, _ := json.Marshal(ThreeXUIClientView{ID: parent, Email: "Phone", Enabled: true, InboundIDs: []int{9}, HasSubscription: true})
	exec(`INSERT INTO three_x_ui_client_accounts(id,controller_id,email,metadata_json,mode,observed_at) VALUES(?,'client-controller','Phone',?,'fixed',?)`, parent, metadata, now)
	if err := store.SelectLanding(ctx, LandingSelection{NodeIDs: []string{owner.ID}, LandingRegionCodes: map[string]string{owner.ID: "US"}}); err != nil {
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
		var peer *landing.PeerIdentity
		if task.LandingServerState.Plan != nil {
			peer = &landing.PeerIdentity{ID: "landing", PublicKey: "landing-key", Address: "100.64.0.9"}
		}
		if err := store.completeLandingServer(ctx, commitProjectionOnlyForTest, owner.ID, task.Revision, task.Attempt, true, peer); err != nil {
			t.Fatal(err)
		}
	}
	completeServer(claimLanding(true))
	grants, err := store.LandingClientGrants(ctx, parent)
	if err != nil || len(grants) != 1 {
		t.Fatalf("global pool did not derive one authorized combination: %+v %v", grants, err)
	}
	grant := grants[0]
	if grant.Status != "preparing" || grant.AppliedRevision != 0 {
		t.Fatalf("grant queued: %+v %v", grant, err)
	}
	// A separate topology refresh can arrive while the controller is still
	// preparing the child. It must leave the grant queued for prepare rather
	// than claiming an entry route whose fixed user does not exist yet.
	preparationTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.queueClientLandingRoutes(ctx, preparationTx, grant.ApplicationID); !errors.Is(err, errLandingRouteApplying) {
		preparationTx.Rollback()
		t.Fatalf("unprepared grant was sent to entry: %v", err)
	}
	if err := preparationTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	grants, err = store.LandingClientGrants(ctx, parent)
	if err != nil || len(grants) != 1 || grants[0].Status != "preparing" {
		t.Fatalf("topology refresh consumed preparation: %+v %v", grants, err)
	}
	assertRetiredProxyCommandChannel(t, store, entry.ID)
	grants, err = store.LandingClientGrants(ctx, parent)
	if err != nil || len(grants) != 1 || grants[0].Status != "preparing" {
		t.Fatal("late legacy result changed grant", err)
	}
}
