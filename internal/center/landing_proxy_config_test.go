package center

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
)

func TestLandingProxyOrdersSourceAuthorizationAndRouteRestoration(t *testing.T) {
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
	owner, proxy := nodes[0].ID, nodes[1].ID
	exec(`UPDATE agents SET tailscale_ownership='managed' WHERE id IN (?,?)`, owner, proxy)
	exec(`UPDATE agent_network_profiles SET headscale_address='100.64.0.8' WHERE agent_id=?`, owner)
	exec(`UPDATE agent_network_profiles SET headscale_address='100.64.0.9' WHERE agent_id=?`, proxy)
	now := store.now().UTC().Format(time.RFC3339Nano)
	site := testSiteID(t, store)
	exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,status,runtime,role,created_at,updated_at) VALUES('landing-app','Proxy',?,?,'vastora-official/3x-ui','running','docker','worker',?,?)`, proxy, site, now, now)
	exec(`INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,app_protocol,status,created_at,updated_at) VALUES('landing-inbound','landing-app',?,'VLESS','tcp',443,443,'unchanged.example:443','observed','vless/tcp/reality','ready',?,?)`, site, now, now)
	exec(`INSERT INTO three_x_ui_inbound_plans(service_id,inbound_tag,updated_at) VALUES('landing-inbound','business',?)`, now)
	claim := func(server bool) *AgentTask {
		t.Helper()
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var task *AgentTask
		if server {
			task, err = store.claimLandingServerTask(ctx, tx, owner)
		} else {
			task, err = store.claimLandingProxyTask(ctx, tx, proxy)
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
			t.Fatal("expected landing server task")
		}
		peer := &landing.PeerIdentity{ID: "peer", PublicKey: "key", Address: "100.64.0.8"}
		if err := store.completeLandingServer(ctx, owner, task.Revision, task.Attempt, true, peer); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SelectLanding(ctx, LandingSelection{NodeID: owner}); err != nil {
		t.Fatal(err)
	}
	completeServer(claim(true))
	target, err := store.landingLatencyTarget(ctx, proxy)
	if err != nil || target == nil || target.Peer.Address != "100.64.0.8" {
		t.Fatalf("disabled proxy must receive the selected latency target: %v", err)
	}
	if self, err := store.landingLatencyTarget(ctx, owner); err != nil || self != nil {
		t.Fatalf("landing server must not probe itself: %v", err)
	}
	if err := store.ConfigureLandingProxy(ctx, "landing-app", LandingProxyInput{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if claim(false) != nil {
		t.Fatal("proxy enabled before landing source authorization")
	}
	authorization := claim(true)
	if authorization == nil || len(authorization.LandingServerState.Plan.Sources) != 1 || authorization.LandingServerState.Plan.Sources[0].Address != "100.64.0.9" {
		t.Fatal("missing exact source authorization")
	}
	completeServer(authorization)
	enable := claim(false)
	if enable == nil || enable.LandingProxyState.Proxy == nil || len(enable.LandingProxyState.Proxy.InboundTags) != 1 || enable.LandingProxyState.Proxy.InboundTags[0] != "business" {
		t.Fatal("missing managed proxy enable task")
	}
	if err := store.completeLandingProxy(ctx, proxy, enable.Revision, enable.Attempt, false); err != nil {
		t.Fatal(err)
	}
	if claim(false) != nil {
		t.Fatal("failed configuration retried without user action")
	}
	if err := store.ConfigureLandingProxy(ctx, "landing-app", LandingProxyInput{Enabled: true, Revision: uint64(enable.Revision)}); err != nil {
		t.Fatal(err)
	}
	retry := claim(false)
	if retry == nil || retry.Revision != enable.Revision || retry.Attempt != enable.Attempt+1 {
		t.Fatal("explicit retry must preserve revision and advance attempt")
	}
	enable = retry
	if err := store.completeLandingProxy(ctx, proxy, enable.Revision, enable.Attempt, true); err != nil {
		t.Fatal(err)
	}
	// Restoring direct routing must remain possible without a current network
	// profile or a healthy landing server. The saved source is removed last.
	exec(`DELETE FROM agent_network_profiles WHERE agent_id=?`, proxy)
	exec(`UPDATE landing_server_states SET status='failed' WHERE node_id=?`, owner)
	if err := store.ConfigureLandingProxy(ctx, "landing-app", LandingProxyInput{Revision: uint64(enable.Revision)}); err != nil {
		t.Fatal(err)
	}
	readServer := func() landing.ServerState {
		t.Helper()
		var raw []byte
		var state landing.ServerState
		if err := store.db.QueryRow(`SELECT desired_json FROM landing_server_states WHERE node_id=?`, owner).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	if len(readServer().Plan.Sources) != 1 {
		t.Fatal("source removed before proxy restoration")
	}
	disable := claim(false)
	if disable == nil || disable.LandingProxyState.Proxy != nil {
		t.Fatal("missing restoration task despite unhealthy landing")
	}
	if err := store.completeLandingProxy(ctx, proxy, disable.Revision, disable.Attempt, true); err != nil {
		t.Fatal(err)
	}
	if len(readServer().Plan.Sources) != 0 {
		t.Fatal("restoration did not revoke saved source")
	}
	var endpoint string
	if err := store.db.QueryRow(`SELECT endpoint FROM services WHERE id='landing-inbound'`).Scan(&endpoint); err != nil || endpoint != "unchanged.example:443" {
		t.Fatal("landing switch changed public entry", err)
	}
}
