package center

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/networking"
)

func TestLegacyMeridianGrantPinsAuthorizedSourceAndMapsPeerToAgent(t *testing.T) {
	store, endpoint, legacy, egressID, source := openMeridianLegacyGrantSourceFixture(t)
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	resolved, err := resolveLegacyMeridianGrant(context.Background(), tx, &endpoint, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != egressID || resolved == legacy.EgressNodeID || endpoint.sourcePeer == nil || *endpoint.sourcePeer != source {
		t.Fatal("legacy import confused Agent and Tailscale identity or lost the authorized source")
	}
	// Repeated grants on an endpoint must agree; the same observation is safe
	// to reuse, but a different authorized source may not overwrite the pin.
	if _, err := resolveLegacyMeridianGrant(context.Background(), tx, &endpoint, legacy); err != nil {
		t.Fatalf("consistent source was rejected: %v", err)
	}
	replacement := source
	replacement.PublicKey = "nodekey:test-replaced-entry"
	encoded, _ := json.Marshal(replacement)
	if _, err := tx.Exec(`UPDATE landing_client_grants SET source_peer_json=? WHERE id=?`, encoded, legacy.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveLegacyMeridianGrant(context.Background(), tx, &endpoint, legacy); err == nil || *endpoint.sourcePeer != source {
		t.Fatal("a conflicting source overwrote the endpoint authority")
	}
}

func TestLegacyMeridianGrantRejectsChangedAuthority(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*meridianImportedEndpoint, *meridianruntime.LegacyRoute)
	}{
		{name: "application", change: func(endpoint *meridianImportedEndpoint, _ *meridianruntime.LegacyRoute) {
			endpoint.applicationID = "another-entry"
		}},
		{name: "service", change: func(endpoint *meridianImportedEndpoint, _ *meridianruntime.LegacyRoute) {
			endpoint.serviceID = "another-service"
		}},
		{name: "parent", change: func(_ *meridianImportedEndpoint, route *meridianruntime.LegacyRoute) {
			route.ParentIdentityHash = meridian.Identity("another-parent")
		}},
		{name: "inbound", change: func(_ *meridianImportedEndpoint, route *meridianruntime.LegacyRoute) {
			route.InboundTag = "another-inbound"
		}},
		{name: "native user", change: func(_ *meridianImportedEndpoint, route *meridianruntime.LegacyRoute) {
			route.BaseUser = "another-native-user"
		}},
		{name: "route user", change: func(_ *meridianImportedEndpoint, route *meridianruntime.LegacyRoute) {
			route.FixedUser = "another-route-user"
		}},
		{name: "route identity", change: func(_ *meridianImportedEndpoint, route *meridianruntime.LegacyRoute) {
			route.FixedUUID = "33333333-3333-4333-8333-333333333333"
		}},
		{name: "egress peer", change: func(_ *meridianImportedEndpoint, route *meridianruntime.LegacyRoute) {
			route.EgressNodeID = "another-tailscale-peer"
		}},
		{name: "revision", change: func(_ *meridianImportedEndpoint, route *meridianruntime.LegacyRoute) { route.Revision++ }},
		{name: "enabled", change: func(_ *meridianImportedEndpoint, route *meridianruntime.LegacyRoute) { route.Enabled = false }},
		{name: "native visibility", change: func(_ *meridianImportedEndpoint, route *meridianruntime.LegacyRoute) { route.HideNative = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, endpoint, legacy, _, _ := openMeridianLegacyGrantSourceFixture(t)
			test.change(&endpoint, &legacy)
			tx, err := store.db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := resolveLegacyMeridianGrant(context.Background(), tx, &endpoint, legacy); err == nil || endpoint.sourcePeer != nil {
				t.Fatal("changed legacy authority was accepted")
			}
		})
	}
}

func TestDisabledLegacyMeridianGrantDoesNotAuthorizeSource(t *testing.T) {
	store, endpoint, legacy, egressID, _ := openMeridianLegacyGrantSourceFixture(t)
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE landing_client_grants SET source_peer_json='{}',grant_json=json_set(grant_json,'$.enabled',json('false')) WHERE id=?`, legacy.ID); err != nil {
		t.Fatal(err)
	}
	legacy.Enabled = false
	resolved, err := resolveLegacyMeridianGrant(context.Background(), tx, &endpoint, legacy)
	if err != nil || resolved != egressID || endpoint.sourcePeer != nil {
		t.Fatalf("disabled route either granted source authority or lost its retained identity: %v", err)
	}
}

func TestEnabledLegacyMeridianGrantCannotUseCurrentCapabilityAsAuthority(t *testing.T) {
	store, endpoint, legacy, _, _ := openMeridianLegacyGrantSourceFixture(t)
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE landing_client_grants SET source_peer_json='{}' WHERE id=?`, legacy.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveLegacyMeridianGrant(context.Background(), tx, &endpoint, legacy); err == nil || endpoint.sourcePeer != nil {
		t.Fatal("current capability silently replaced missing legacy authorization")
	}
}

func openMeridianLegacyGrantSourceFixture(t *testing.T) (*Store, meridianImportedEndpoint, meridianruntime.LegacyRoute, string, landing.PeerIdentity) {
	t.Helper()
	store := openMeridianSharedEndpointSnapshotFixture(t)
	ctx := context.Background()
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	egress := enrollOrchestrationNode(t, store, "legacy-source-egress", NodeCapabilities{Docker: true},
		[]networking.Candidate{{Address: "100.64.0.63", Interface: "tailscale0", Kind: networking.KindHeadscale}},
		networking.Profile{ServiceAddress: "100.64.0.63", HeadscaleAddress: "100.64.0.63", EnabledKinds: []string{networking.KindHeadscale}})
	const baseUUID = "11111111-1111-4111-8111-111111111111"
	const routeUUID = "22222222-2222-4222-8222-222222222222"
	parentID := landing.Identity(baseUUID)
	source := landing.PeerIdentity{ID: "tailscale-legacy-entry", PublicKey: "nodekey:test-legacy-entry", Address: "100.64.0.61"}
	peer := landing.PeerIdentity{ID: "tailscale-legacy-egress", PublicKey: "nodekey:test-legacy-egress", Address: "100.64.0.63"}
	grant := landing.ClientGrant{ID: "legacy-source-grant", ParentID: parentID, InboundTag: "shared-entry", BaseUser: "Original account", BaseIdentity: parentID,
		FixedUser: landing.FixedUser("legacy-source-grant"), FixedIdentity: landing.Identity(routeUUID), Peer: peer, Mode: landing.FixedMode, Enabled: true}
	grantJSON, _ := json.Marshal(grant)
	sourceJSON, _ := json.Marshal(source)
	var entryID, secretID string
	if err := store.db.QueryRowContext(ctx, `SELECT application.node_id,endpoint.private_key_secret_id FROM applications application JOIN meridian_endpoints endpoint ON endpoint.application_id=application.id WHERE endpoint.id=?`, sharedSnapshotEndpointID).Scan(&entryID, &secretID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE landing_client_capabilities SET generation=?,peer_json=?,observed_at=? WHERE node_id=?`, landing.ClientRuntimeGeneration, sourceJSON, stamp, entryID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO three_x_ui_client_accounts(id,controller_id,email,metadata_json,observed_at) VALUES(?,'snapshot-shared-app','Original account','{}',?)`, parentID, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO landing_client_grants(id,parent_id,application_id,service_id,landing_node_id,source_peer_json,grant_json,credential_secret_id,desired_revision,applied_revision,status,updated_at)
		VALUES(?,?,'snapshot-shared-app','snapshot-shared-service',?,?,?,?,4,4,'ready',?)`, grant.ID, parentID, egress.ID, sourceJSON, grantJSON, secretID, stamp); err != nil {
		t.Fatal(err)
	}
	endpoint := meridianImportedEndpoint{model: meridian.RealityEndpoint{ID: sharedSnapshotEndpointID, InboundTag: "shared-entry"}, applicationID: "snapshot-shared-app", serviceID: "snapshot-shared-service", nodeID: entryID}
	legacy := meridianruntime.LegacyRoute{ID: grant.ID, ParentIdentityHash: parentID, InboundID: 1, InboundTag: grant.InboundTag, BaseUser: grant.BaseUser, FixedUser: grant.FixedUser,
		FixedUUID: routeUUID, EgressNodeID: peer.ID, Enabled: true, Revision: 4}
	return store, endpoint, legacy, egress.ID, source
}
