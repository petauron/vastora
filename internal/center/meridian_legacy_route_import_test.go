package center

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
)

func TestMeridianLegacyRouteImportPreservesReadyCredentialAndBlocksEarlyRetirement(t *testing.T) {
	store, endpoint, legacy, egressID, source := openMeridianLegacyGrantSourceFixture(t)
	prepareMeridianLegacyRouteImport(t, store, legacy, egressID)
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	unimported, err := meridianUnimportedReadyLegacyRoutes(ctx, tx)
	if err != nil || unimported != 1 {
		t.Fatalf("ready legacy route was not fenced: unimported=%d err=%v", unimported, err)
	}
	imported, err := store.importReadyMeridianLegacyRoutesInTx(ctx, tx, store.now().UTC())
	if err != nil || imported != 1 {
		t.Fatalf("ready route did not import atomically: imported=%d err=%v", imported, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	unimported, err = meridianUnimportedReadyLegacyRoutes(ctx, tx)
	if err != nil || unimported != 0 {
		t.Fatalf("imported route still blocks retirement: unimported=%d err=%v", unimported, err)
	}
	var routeCredentialID, fixedID, accountID, egressNodeID, sourceJSON string
	var baseline, observed, expectedRoutes, expectedCredentials, desiredRevision int
	if err := tx.QueryRowContext(ctx, "SELECT credential.id,credential.protocol_secret_id,grant_row.account_id,grant_row.egress_node_id "+
		"FROM meridian_route_grants grant_row JOIN meridian_credentials credential ON credential.id=grant_row.route_credential_id "+
		"WHERE grant_row.id=?", legacy.ID).Scan(&routeCredentialID, &fixedID, &accountID, &egressNodeID); err != nil {
		t.Fatal(err)
	}
	protocolID, err := store.meridianSecretInTx(ctx, tx, fixedID, meridianCredentialSecretContext(routeCredentialID))
	if err != nil || string(protocolID) != legacy.FixedUUID || egressNodeID != egressID || accountID == "" {
		t.Fatalf("fixed credential or egress changed: egress=%q account=%q err=%v", egressNodeID, accountID, err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT watermark.baseline_bytes,watermark.observed_bytes FROM meridian_usage_watermarks watermark "+
		"JOIN meridian_route_grants grant_row ON grant_row.route_credential_id=watermark.credential_id WHERE grant_row.id=?", legacy.ID).
		Scan(&baseline, &observed); err != nil || baseline != 0 || observed != 0 {
		t.Fatalf("historical shared bytes would be counted twice: baseline=%d observed=%d err=%v", baseline, observed, err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT source_peer_json,desired_revision FROM meridian_endpoints WHERE id=?", endpoint.model.ID).
		Scan(&sourceJSON, &desiredRevision); err != nil || desiredRevision != 2 {
		t.Fatalf("entry was not queued for route projection: revision=%d err=%v", desiredRevision, err)
	}
	var importedSource landing.PeerIdentity
	if json.Unmarshal([]byte(sourceJSON), &importedSource) != nil || importedSource != source {
		t.Fatal("legacy entry source was not pinned")
	}
	if err := tx.QueryRowContext(ctx, "SELECT expected_routes,expected_credentials FROM meridian_cutover WHERE id=1").
		Scan(&expectedRoutes, &expectedCredentials); err != nil || expectedRoutes != 1 || expectedCredentials != 3 {
		t.Fatalf("cutover counts were not advanced: routes=%d credentials=%d err=%v", expectedRoutes, expectedCredentials, err)
	}
	again, err := store.importReadyMeridianLegacyRoutesInTx(ctx, tx, store.now().UTC())
	if err != nil || again != 0 {
		t.Fatalf("legacy route import replayed: imported=%d err=%v", again, err)
	}
}

func TestMeridianLegacyRouteImportRejectsChangedReceiptWithoutWrites(t *testing.T) {
	store, endpoint, legacy, egressID, _ := openMeridianLegacyGrantSourceFixture(t)
	prepareMeridianLegacyRouteImport(t, store, legacy, egressID)
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	badReceipt, _ := json.Marshal(landing.ControllerResult{GrantID: legacy.ID, Revision: legacy.Revision, Phase: "activate",
		BaseLink:          "vless://11111111-1111-4111-8111-111111111111@entry.example.test:443?security=reality",
		FixedLink:         "vless://33333333-3333-4333-8333-333333333333@entry.example.test:443?security=reality",
		SubscriptionToken: "subscription-token"})
	badSecretID, err := store.putSecret(ctx, tx, badReceipt, "landing-material:"+legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE landing_client_grants SET material_secret_id=? WHERE id=?", badSecretID, legacy.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.importReadyMeridianLegacyRoutesInTx(ctx, tx, store.now().UTC()); err == nil {
		t.Fatal("changed activation receipt was imported")
	}
	var count, revision int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM meridian_route_grants").Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed import wrote a route: count=%d err=%v", count, err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT desired_revision FROM meridian_endpoints WHERE id=?", endpoint.model.ID).Scan(&revision); err != nil || revision != 1 {
		t.Fatalf("failed import changed endpoint: revision=%d err=%v", revision, err)
	}
}

func prepareMeridianLegacyRouteImport(t *testing.T, store *Store, legacy meridianruntime.LegacyRoute, egressID string) {
	t.Helper()
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	record, err := readLandingGrant(ctx, tx, legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	credentialSecretID, err := store.putSecret(ctx, tx, []byte(legacy.FixedUUID), "landing-credential:"+legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, _ := json.Marshal(landing.ControllerResult{GrantID: legacy.ID, Revision: legacy.Revision, Phase: "activate",
		BaseLink:          "vless://11111111-1111-4111-8111-111111111111@entry.example.test:443?security=reality",
		FixedLink:         "vless://" + legacy.FixedUUID + "@entry.example.test:443?security=reality",
		SubscriptionToken: "subscription-token"})
	materialSecretID, err := store.putSecret(ctx, tx, receipt, "landing-material:"+legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, "UPDATE landing_client_grants SET credential_secret_id=?,material_secret_id=? WHERE id=?",
		credentialSecretID, materialSecretID, legacy.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE meridian_accounts SET display_name=? WHERE id IN "+
		"(SELECT account_id FROM meridian_credentials WHERE kind='native' AND identity_sha256=?)", record.Grant.BaseUser, legacy.ParentIdentityHash); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE agents SET tailscale_ownership='managed',last_seen_at=? WHERE id=?", stamp, egressID); err != nil {
		t.Fatal(err)
	}
	peerJSON, _ := json.Marshal(record.Grant.Peer)
	serverJSON, _ := json.Marshal(landing.ServerState{NodeID: egressID, Revision: 1, Plan: &landing.ServerPlan{
		Revision: 1, Address: record.Grant.Peer.Address, Sources: []landing.AuthorizedNode{{Address: record.Source.Address, TCPOnly: true}},
	}})
	if _, err := tx.ExecContext(ctx, "INSERT INTO landing_server_states(node_id,desired_revision,applied_revision,desired_json,applied_json,peer_json,status,updated_at) "+
		"VALUES(?,1,1,?,?,?,'ready',?)", egressID, serverJSON, serverJSON, peerJSON, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE meridian_cutover SET state='verify',subscription_authority='meridian',expected_routes=0,expected_credentials=2 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
