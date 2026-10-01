package center

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/nodediagnostics"
)

func TestMeridianLinkBandwidthQueuesBothPrivatePeers(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	capabilities := NodeCapabilities{Docker: true, MeridianLinkBandwidth: true, MeridianLinkRevision: nodediagnostics.LinkBandwidthRevision}
	newNode := func(name, address string) AgentCredential {
		return enrollOrchestrationNode(t, store, name, capabilities,
			[]networking.Candidate{{Address: address, Interface: "tailscale0", Kind: networking.KindHeadscale}},
			networking.Profile{ServiceAddress: address, HeadscaleAddress: address, EnabledKinds: []string{networking.KindHeadscale}})
	}
	source := newNode("link-source", "100.64.0.81")
	egress := newNode("link-egress", "100.64.0.82")
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	for _, node := range []AgentCredential{source, egress} {
		if _, err := store.db.ExecContext(ctx, `UPDATE agents SET tailscale_ownership='managed',last_seen_at=? WHERE id=?`, stamp, node.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SelectLanding(ctx, LandingSelection{NodeIDs: []string{egress.ID}, LandingRegionCodes: map[string]string{egress.ID: "US"}}); err != nil {
		t.Fatal(err)
	}
	sourcePeer, _ := json.Marshal(landing.PeerIdentity{ID: "ts-source", PublicKey: "nodekey:source", Address: "100.64.0.81"})
	egressPeer, _ := json.Marshal(landing.PeerIdentity{ID: "ts-egress", PublicKey: "nodekey:egress", Address: "100.64.0.82"})
	if _, err := store.db.ExecContext(ctx, `INSERT INTO agent_private_peer_capabilities(node_id,generation,peer_json,observed_at) VALUES(?,1,?,?)`, source.ID, sourcePeer, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE landing_server_states SET status='ready',applied_revision=desired_revision,peer_json=? WHERE node_id=?`, egressPeer, egress.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,status,runtime,role,created_at,updated_at) VALUES('link-app','Meridian entry',?,?,'vastora-official/meridian','running','docker','',?,?)`, source.ID, testSiteID(t, store), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	// Old Agents must not receive the new execution contract during rollout.
	if _, err := store.db.Exec(`UPDATE agents SET capabilities_json=json_set(capabilities_json,'$.meridianLinkRevision',0) WHERE id=?`, source.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.StartMeridianLinkBandwidth(ctx, source.ID, egress.ID); err == nil {
		t.Fatal("old link contract accepted")
	}
	if _, err := store.db.Exec(`UPDATE agents SET capabilities_json=json_set(capabilities_json,'$.meridianLinkRevision',?) WHERE id=?`, nodediagnostics.LinkBandwidthRevision, source.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.StartMeridianLinkBandwidth(ctx, source.ID, egress.ID); err != nil {
		t.Fatal(err)
	}
	var rawTarget []byte
	if err := store.db.QueryRow(`SELECT targets_json FROM node_diagnostic_checks WHERE agent_id=? AND kind=?`, source.ID, nodediagnostics.LinkBandwidthKind).Scan(&rawTarget); err != nil {
		t.Fatal(err)
	}
	var link nodediagnostics.LinkBandwidthTask
	if json.Unmarshal(rawTarget, &link) != nil || (nodediagnostics.Task{Link: &link}).ValidateLinkIdentity(nodediagnostics.LinkBandwidthKind) != nil {
		t.Fatal("task lost authenticated peer identities")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !currentMeridianLinkPeers(ctx, tx, link) {
		t.Fatal("valid peer identities rejected")
	}
	if _, err := tx.Exec(`UPDATE agent_private_peer_capabilities SET peer_json=json_set(peer_json,'$.publicKey','nodekey:replacement') WHERE node_id=?`, source.ID); err != nil {
		t.Fatal(err)
	}
	if currentMeridianLinkPeers(ctx, tx, link) {
		t.Fatal("reinstalled peer accepted for old measurement")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	checks, err := store.ListNodeDiagnostics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var paired int
	for _, check := range checks {
		if check.Kind != nodediagnostics.LinkBandwidthKind && check.Kind != nodediagnostics.LinkServerKind {
			continue
		}
		paired++
		if check.State != "pending" || check.ID == "" || check.TargetRevision != nodediagnostics.LinkBandwidthRevision {
			t.Fatalf("unready link task: %+v", check)
		}
	}
	if paired != 2 {
		t.Fatalf("wanted source and landing tasks, got %d", paired)
	}
	if err := store.StartMeridianLinkBandwidth(ctx, source.ID, egress.ID); err == nil {
		t.Fatal("concurrent link check accepted")
	}
	// A later test to another landing must not erase the first pair or its peer.
	if _, err := store.db.Exec(`UPDATE node_diagnostic_checks SET state='succeeded'`); err != nil {
		t.Fatal(err)
	}
	other := newNode("other-egress", "100.64.0.83")
	if _, err := store.db.Exec(`UPDATE agents SET tailscale_ownership='managed',last_seen_at=? WHERE id=?`, stamp, other.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SelectLanding(ctx, LandingSelection{Revision: 1, NodeIDs: []string{egress.ID, other.ID}, LandingRegionCodes: map[string]string{egress.ID: "US", other.ID: "US"}}); err != nil {
		t.Fatal(err)
	}
	otherPeer, _ := json.Marshal(landing.PeerIdentity{ID: "ts-other", PublicKey: "nodekey:other", Address: "100.64.0.83"})
	if _, err := store.db.Exec(`UPDATE landing_server_states SET status='ready',applied_revision=desired_revision,peer_json=? WHERE node_id=?`, otherPeer, other.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.StartMeridianLinkBandwidth(ctx, source.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	checks, err = store.ListNodeDiagnostics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pairs := map[string]NodeDiagnosticView{}
	for _, check := range checks {
		if check.AgentID == source.ID && check.Kind == nodediagnostics.LinkBandwidthKind {
			pairs[check.LandingNodeID] = check
		}
	}
	if len(pairs) != 2 || pairs[egress.ID].State != "succeeded" || pairs[other.ID].State != "pending" {
		t.Fatalf("link pairs overwritten or invalidated: %+v", pairs)
	}
}
