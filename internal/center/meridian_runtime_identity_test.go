package center

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
)

func TestMeridianRuntimeRoutesKeepAgentAndTailscaleIdentitySeparate(t *testing.T) {
	store, egressID, grantID, peer := openMeridianRuntimeIdentityFixture(t)
	if egressID == peer.ID {
		t.Fatal("fixture must use distinct Agent and Tailscale IDs")
	}
	routes, err := readMeridianRuntimeIdentityRoutes(t, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes.grants) != 1 || len(routes.peers) != 1 || len(routes.readyGrantIDs) != 1 || routes.readyGrantIDs[0] != grantID {
		t.Fatalf("ready landing was not projected: grants=%d peers=%d ready=%d", len(routes.grants), len(routes.peers), len(routes.readyGrantIDs))
	}
	if routes.grants[0].EgressID != egressID || routes.grants[0].Route.EgressID != egressID || routes.peers[0].EgressID != egressID {
		t.Fatal("Meridian grant or SOCKS projection used a Tailscale ID instead of the Agent ID")
	}
	if routes.peers[0].Address != peer.Address || routes.peers[0].Port != landing.SOCKSPort {
		t.Fatal("Meridian projection lost the separately authenticated landing address")
	}
	if len(routes.blockedGrants) != 0 || len(routes.disabledCredentialIDs) != 0 {
		t.Fatal("distinct identity domains incorrectly blocked a ready landing")
	}
}

func TestMeridianRuntimeRoutesRejectIncompleteLandingIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*landing.PeerIdentity)
	}{
		{name: "missing Tailscale ID", change: func(peer *landing.PeerIdentity) { peer.ID = "" }},
		{name: "missing Tailscale public key", change: func(peer *landing.PeerIdentity) { peer.PublicKey = "" }},
		{name: "missing private address", change: func(peer *landing.PeerIdentity) { peer.Address = "" }},
		{name: "non-tailnet address", change: func(peer *landing.PeerIdentity) { peer.Address = "10.0.0.62" }},
		{name: "public address", change: func(peer *landing.PeerIdentity) { peer.Address = "203.0.113.62" }},
		{name: "IPv6 address", change: func(peer *landing.PeerIdentity) { peer.Address = "fd00::62" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, egressID, _, peer := openMeridianRuntimeIdentityFixture(t)
			test.change(&peer)
			encoded, err := json.Marshal(peer)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(context.Background(), `UPDATE landing_server_states SET peer_json=? WHERE node_id=?`, encoded, egressID); err != nil {
				t.Fatal(err)
			}
			if _, err := readMeridianRuntimeIdentityRoutes(t, store, nil); err == nil {
				t.Fatal("invalid landing identity was accepted")
			}
		})
	}
}

func TestMeridianRuntimeRoutesStillRequireMatchingCredentialAgentID(t *testing.T) {
	store, _, _, _ := openMeridianRuntimeIdentityFixture(t)
	_, err := readMeridianRuntimeIdentityRoutes(t, store, func(credentials map[string]meridian.CredentialMaterial) {
		for id, credential := range credentials {
			if credential.Credential.Kind == meridian.RouteCredential {
				credential.Credential.EgressID = "another-vastora-agent"
				credentials[id] = credential
			}
		}
	})
	if err == nil {
		t.Fatal("route credential with another Agent ID was accepted")
	}
}

func openMeridianRuntimeIdentityFixture(t *testing.T) (*Store, string, string, landing.PeerIdentity) {
	t.Helper()
	store := openMeridianSharedEndpointSnapshotFixture(t)
	egress := enrollOrchestrationNode(t, store, "runtime-identity-egress", NodeCapabilities{Docker: true},
		[]networking.Candidate{{Address: "100.64.0.62", Interface: "tailscale0", Kind: networking.KindHeadscale}},
		networking.Profile{ServiceAddress: "100.64.0.62", HeadscaleAddress: "100.64.0.62", EnabledKinds: []string{networking.KindHeadscale}})
	ctx := context.Background()
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `UPDATE agents SET tailscale_ownership='managed',last_seen_at=? WHERE id=?`, stamp, egress.ID); err != nil {
		t.Fatal(err)
	}
	peer := landing.PeerIdentity{ID: "tailscale-runtime-egress", PublicKey: "nodekey:test-runtime-egress", Address: "100.64.0.62"}
	peerJSON, err := json.Marshal(peer)
	if err != nil {
		t.Fatal(err)
	}
	server := landing.ServerState{NodeID: egress.ID, Revision: 1, Plan: &landing.ServerPlan{Revision: 1, Address: peer.Address, Sources: []landing.AuthorizedNode{{Address: "100.64.0.61", TCPOnly: true}}}}
	serverJSON, err := json.Marshal(server)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO landing_server_states(node_id,desired_revision,applied_revision,desired_json,applied_json,peer_json,status,updated_at)
		VALUES(?,1,1,?,?,?,'ready',?)`, egress.ID, serverJSON, serverJSON, peerJSON, stamp); err != nil {
		t.Fatal(err)
	}
	grant, err := store.CreateMeridianRouteGrant(ctx, MeridianRouteGrantInput{AccountID: sharedSnapshotAccountA, EndpointID: sharedSnapshotEndpointID, EgressNodeID: egress.ID})
	if err != nil {
		t.Fatal(err)
	}
	return store, egress.ID, grant.ID, peer
}

func readMeridianRuntimeIdentityRoutes(t *testing.T, store *Store, change func(map[string]meridian.CredentialMaterial)) (meridianRuntimeRoutes, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, credentials, err := store.meridianRuntimeMaterials(ctx, tx, sharedSnapshotEndpointID, "snapshot-shared-app")
	if err != nil {
		t.Fatal(err)
	}
	if change != nil {
		change(credentials)
	}
	return store.meridianRuntimeGrants(ctx, tx, sharedSnapshotEndpointID, "shared-entry", credentials)
}
