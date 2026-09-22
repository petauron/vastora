package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
)

const meridianSourcesGrantID = "meridian-source-union-grant"

func openMeridianLandingSourcesFixture(t *testing.T) (*Store, string, string) {
	t.Helper()
	store := openMeridianSharedEndpointSnapshotFixture(t)
	ctx := context.Background()
	egress := enrollOrchestrationNode(t, store, "source-union-egress", NodeCapabilities{Docker: true},
		[]networking.Candidate{{Address: "100.64.0.63", Interface: "tailscale0", Kind: networking.KindHeadscale}},
		networking.Profile{ServiceAddress: "100.64.0.63", HeadscaleAddress: "100.64.0.63", EnabledKinds: []string{networking.KindHeadscale}})
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var entryID string
	var sourceJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT application.node_id,capability.peer_json FROM applications application
		JOIN landing_client_capabilities capability ON capability.node_id=application.node_id WHERE application.id='snapshot-shared-app'`).Scan(&entryID, &sourceJSON); err != nil {
		t.Fatal(err)
	}
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET tailscale_ownership='managed' WHERE id=?`, egress.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET source_peer_json=? WHERE id=?`, sourceJSON, sharedSnapshotEndpointID); err != nil {
		t.Fatal(err)
	}
	server := landing.ServerState{NodeID: egress.ID, Revision: 1, Plan: &landing.ServerPlan{Revision: 1, Address: "100.64.0.63"}}
	serverJSON, err := json.Marshal(server)
	if err != nil || server.Validate() != nil {
		t.Fatal("invalid source-union fixture server", err)
	}
	peerJSON, _ := json.Marshal(landing.PeerIdentity{ID: "tailnet-source-union-egress", PublicKey: "nodekey:source-union-egress", Address: "100.64.0.63"})
	if _, err := tx.ExecContext(ctx, `INSERT INTO landing_server_states(node_id,desired_revision,applied_revision,desired_json,applied_json,peer_json,status,updated_at)
		VALUES(?,1,1,?,?,?,'ready',?)`, egress.ID, serverJSON, serverJSON, peerJSON, stamp); err != nil {
		t.Fatal(err)
	}
	credentialID, err := store.insertMeridianCredential(ctx, tx, sharedSnapshotAccountB, sharedSnapshotEndpointID, "snapshot-shared-app", meridian.RouteCredential, egress.ID, true, stamp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_route_grants(id,account_id,endpoint_id,egress_node_id,base_credential_id,route_credential_id,enabled,status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,1,'pending',?,?)`, meridianSourcesGrantID, sharedSnapshotAccountB, sharedSnapshotEndpointID, egress.ID, sharedSnapshotAccountB+"-native", credentialID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return store, entryID, egress.ID
}

func meridianLandingSourcePlan(t *testing.T, tx *sql.Tx, egressID string) landing.ServerState {
	t.Helper()
	var encoded []byte
	if err := tx.QueryRow(`SELECT desired_json FROM landing_server_states WHERE node_id=?`, egressID).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var state landing.ServerState
	if err := json.Unmarshal(encoded, &state); err != nil || state.Validate() != nil || state.Plan == nil {
		t.Fatal("invalid queued source plan", err)
	}
	return state
}

func TestMeridianLandingSourceRefreshResumesAfterCutover(t *testing.T) {
	store, _, egressID := openMeridianLandingSourcesFixture(t)
	ctx := context.Background()
	for _, stage := range []string{"not_required", "backup", "import", "publish", "project", "verify", "retire", "complete"} {
		t.Run(stage, func(t *testing.T) {
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec(`UPDATE meridian_cutover SET state=? WHERE id=1`, stage); err != nil {
				t.Fatal(err)
			}
			if err := store.refreshClientLandingSources(ctx, tx, egressID); err != nil {
				t.Fatal(err)
			}
			state := meridianLandingSourcePlan(t, tx, egressID)
			if stage != "complete" && stage != "not_required" {
				if state.Revision != 1 || len(state.Plan.Sources) != 0 {
					t.Fatalf("cutover stage changed source authority: %#v", state)
				}
				return
			}
			want := []landing.AuthorizedNode{{Address: "100.64.0.61", TCPOnly: true}}
			if state.Revision != 2 || !slices.Equal(state.Plan.Sources, want) {
				t.Fatalf("Meridian source was not queued after handover: %#v", state)
			}
			if err := store.refreshClientLandingSources(ctx, tx, egressID); err != nil {
				t.Fatal(err)
			}
			if next := meridianLandingSourcePlan(t, tx, egressID); next.Revision != state.Revision {
				t.Fatal("unchanged source union created another revision")
			}
		})
	}
}

func TestMeridianLandingSourceUsesPinnedIdentityAndDurableRoutePurpose(t *testing.T) {
	store, entryID, egressID := openMeridianLandingSourcesFixture(t)
	ctx := context.Background()
	cases := []struct {
		name, query string
		args        []any
		want        bool
	}{
		{"blocked peer", `UPDATE meridian_route_grants SET status='blocked',runtime_healthy=0,health_expires_unix_ms=0`, nil, true},
		{"offline entry", `UPDATE agents SET last_seen_at='2000-01-01T00:00:00Z' WHERE id=?`, []any{entryID}, true},
		{"disabled account", `UPDATE meridian_accounts SET enabled=0,status='disabled'`, nil, true},
		{"expired account", `UPDATE meridian_accounts SET expiry_time=1,status='expired'`, nil, true},
		{"disabled credential", `UPDATE meridian_credentials SET enabled=0`, nil, true},
		{"revocation awaiting apply", `UPDATE meridian_route_grants SET enabled=0,status='revoking'`, nil, true},
		{"revocation applied", `UPDATE meridian_route_grants SET enabled=0,status='revoked'`, nil, false},
		{"disabled grant", `UPDATE meridian_route_grants SET enabled=0`, nil, false},
		{"entry removed", `UPDATE agents SET status='disabled' WHERE id=?`, []any{entryID}, false},
		{"entry credential revoked", `UPDATE agents SET credential_revoked_at='2026-09-22T00:00:00Z' WHERE id=?`, []any{entryID}, false},
		{"unmanaged entry", `UPDATE agents SET tailscale_ownership='external' WHERE id=?`, []any{entryID}, false},
		{"different reported identity", `UPDATE landing_client_capabilities SET peer_json='{"id":"changed","publicKey":"changed-key","address":"100.64.0.72"}'`, nil, false},
		{"reported key changed at same address", `UPDATE landing_client_capabilities SET peer_json=json_set(peer_json,'$.publicKey','nodekey:changed')`, nil, false},
		{"identity not observed", `UPDATE landing_client_capabilities SET peer_json='{}'`, nil, true},
		{"capability absent", `DELETE FROM landing_client_capabilities WHERE node_id=?`, []any{entryID}, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec(test.query, test.args...); err != nil {
				t.Fatal(err)
			}
			if err := store.refreshClientLandingSources(ctx, tx, egressID); err != nil {
				t.Fatal(err)
			}
			state := meridianLandingSourcePlan(t, tx, egressID)
			var want []landing.AuthorizedNode
			if test.want {
				want = []landing.AuthorizedNode{{Address: "100.64.0.61", TCPOnly: true}}
			}
			if !slices.Equal(state.Plan.Sources, want) {
				t.Fatalf("wrong durable source union: got=%#v want=%#v", state.Plan.Sources, want)
			}
		})
	}
}

func TestMeridianLandingSourceRejectsMissingPinWithoutCapabilityFallback(t *testing.T) {
	store, _, egressID := openMeridianLandingSourcesFixture(t)
	ctx := context.Background()
	for _, pin := range []string{`{}`, `{"id":"source","publicKey":"key","address":"1.1.1.1"}`, `{"id":"source","address":"100.64.0.61"}`} {
		t.Run(pin, func(t *testing.T) {
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.Exec(`UPDATE meridian_endpoints SET source_peer_json=?`, pin); err != nil {
				t.Fatal(err)
			}
			if err := store.refreshClientLandingSources(ctx, tx, egressID); err == nil {
				t.Fatal("invalid pin was accepted or replaced from capability")
			}
			if state := meridianLandingSourcePlan(t, tx, egressID); state.Revision != 1 || len(state.Plan.Sources) != 0 {
				t.Fatal("invalid pin changed source authorization")
			}
		})
	}
}

func TestMeridianLandingSourceUnionKeepsOtherProtocolPurposes(t *testing.T) {
	store, entryID, egressID := openMeridianLandingSourcesFixture(t)
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	proxy := landing.DesiredState{NodeID: entryID, Revision: 1, Proxy: &landing.ProxyPlan{
		ApplicationID: "snapshot-shared-app", InboundTags: []string{"shared-entry"},
		Peer: landing.PeerIdentity{ID: "tailnet-source-union-egress", PublicKey: "nodekey:source-union-egress", Address: "100.64.0.63"},
	}}
	proxyJSON, err := json.Marshal(proxy)
	if err != nil || proxy.Validate() != nil {
		t.Fatal("invalid source-union fixture proxy", err)
	}
	if _, err := tx.Exec(`INSERT INTO landing_proxy_states(node_id,application_id,landing_node_id,server_revision,source_address,desired_revision,desired_json,status,updated_at)
		VALUES(?,'snapshot-shared-app',?,1,'100.64.0.61',1,?,'ready',?)`, entryID, egressID, proxyJSON, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO landing_proxy_retirements(node_id,landing_node_id,source_address) VALUES(?,?,'100.64.0.64')`, entryID, egressID); err != nil {
		t.Fatal(err)
	}
	check := func(want []landing.AuthorizedNode) {
		t.Helper()
		if err := store.removeLandingSource(ctx, tx, egressID, "100.64.0.61"); err != nil {
			t.Fatal(err)
		}
		if got := meridianLandingSourcePlan(t, tx, egressID).Plan.Sources; !slices.Equal(got, want) {
			t.Fatalf("removing one purpose changed another: got=%#v want=%#v", got, want)
		}
	}
	check([]landing.AuthorizedNode{{Address: "100.64.0.61"}, {Address: "100.64.0.64"}})
	proxy.Proxy = nil
	proxyJSON, _ = json.Marshal(proxy)
	if _, err := tx.Exec(`UPDATE landing_proxy_states SET desired_json=? WHERE node_id=?`, proxyJSON, entryID); err != nil {
		t.Fatal(err)
	}
	check([]landing.AuthorizedNode{{Address: "100.64.0.61", TCPOnly: true}, {Address: "100.64.0.64"}})
	if _, err := tx.Exec(`UPDATE meridian_route_grants SET enabled=0,status='revoking'`); err != nil {
		t.Fatal(err)
	}
	check([]landing.AuthorizedNode{{Address: "100.64.0.61", TCPOnly: true}, {Address: "100.64.0.64"}})
	if _, err := tx.Exec(`UPDATE meridian_route_grants SET status='revoked'`); err != nil {
		t.Fatal(err)
	}
	check([]landing.AuthorizedNode{{Address: "100.64.0.64"}})
	if _, err := tx.Exec(`DELETE FROM landing_proxy_retirements WHERE node_id=?`, entryID); err != nil {
		t.Fatal(err)
	}
	check(nil)
}

func TestMeridianLandingReconciliationFindsEntryAndEgressWithoutLegacyGrants(t *testing.T) {
	store, entryID, egressID := openMeridianLandingSourcesFixture(t)
	ctx := context.Background()
	for _, nodeID := range []string{entryID, egressID} {
		t.Run(nodeID, func(t *testing.T) {
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := store.reconcileClientLandingSourcesForNode(ctx, tx, nodeID); err != nil {
				t.Fatal(err)
			}
			if got := meridianLandingSourcePlan(t, tx, egressID).Plan.Sources; !slices.Equal(got, []landing.AuthorizedNode{{Address: "100.64.0.61", TCPOnly: true}}) {
				t.Fatalf("Meridian-only association was missed: %#v", got)
			}
			if _, err := tx.Exec(`UPDATE meridian_route_grants SET enabled=0,status='revoked'`); err != nil {
				t.Fatal(err)
			}
			if err := store.reconcileClientLandingSourcesForNode(ctx, tx, nodeID); err != nil {
				t.Fatal(err)
			}
			if got := meridianLandingSourcePlan(t, tx, egressID).Plan.Sources; len(got) != 0 {
				t.Fatalf("revoked association was omitted from source cleanup: %#v", got)
			}
		})
	}
}

func TestMeridianLegacyRetirementStopsOnlyTheMatchingLegacySourceUse(t *testing.T) {
	store, entryID, egressID := openMeridianLandingSourcesFixture(t)
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	source := landing.PeerIdentity{ID: "old-source", PublicKey: "old-source-key", Address: "100.64.0.62"}
	sourceJSON, _ := json.Marshal(source)
	if _, err := tx.Exec(`UPDATE landing_client_capabilities SET peer_json=? WHERE node_id=?`, sourceJSON, entryID); err != nil {
		t.Fatal(err)
	}
	var targetJSON []byte
	var secretID string
	if err := tx.QueryRow(`SELECT peer_json FROM landing_server_states WHERE node_id=?`, egressID).Scan(&targetJSON); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(`SELECT private_key_secret_id FROM meridian_endpoints WHERE id=?`, sharedSnapshotEndpointID).Scan(&secretID); err != nil {
		t.Fatal(err)
	}
	var target landing.PeerIdentity
	if err := json.Unmarshal(targetJSON, &target); err != nil {
		t.Fatal(err)
	}
	grant := landing.ClientGrant{ID: "source-union-legacy-grant", ParentID: landing.Identity("11111111-1111-4111-8111-111111111111"), InboundTag: "shared-entry",
		BaseUser: "legacy-base", BaseIdentity: landing.Identity("11111111-1111-4111-8111-111111111111"),
		FixedUser: landing.FixedUser("source-union-legacy-grant"), FixedIdentity: landing.Identity("33333333-3333-4333-8333-333333333333"),
		Peer: target, Mode: landing.FixedMode, Enabled: true}
	grantJSON, err := json.Marshal(grant)
	if err != nil || grant.Validate() != nil {
		t.Fatal("invalid legacy source fixture", err)
	}
	if _, err := tx.Exec(`INSERT INTO three_x_ui_client_accounts(id,controller_id,email,metadata_json,observed_at)
		VALUES(?,'snapshot-shared-app','Legacy','{}',?)`, grant.ParentID, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO landing_client_grants(id,parent_id,application_id,service_id,landing_node_id,source_peer_json,grant_json,credential_secret_id,status,updated_at)
		VALUES('source-union-legacy-grant',?,'snapshot-shared-app','snapshot-shared-service',?,?,?,?,'ready',?)`, grant.ParentID, egressID, sourceJSON, grantJSON, secretID, stamp); err != nil {
		t.Fatal(err)
	}
	check := func(want []landing.AuthorizedNode) {
		t.Helper()
		if err := store.refreshClientLandingSources(ctx, tx, egressID); err != nil {
			t.Fatal(err)
		}
		if got := meridianLandingSourcePlan(t, tx, egressID).Plan.Sources; !slices.Equal(got, want) {
			t.Fatalf("incorrect legacy/Meridian source union: got=%#v want=%#v", got, want)
		}
	}
	// The legacy source matches its authenticated observation. The Meridian
	// pin differs and must not authorize either its old or replacement address.
	legacySource := []landing.AuthorizedNode{{Address: "100.64.0.62", TCPOnly: true}}
	check(legacySource)
	if _, err := tx.Exec(`UPDATE meridian_endpoints SET legacy_retired=1 WHERE id=?`, sharedSnapshotEndpointID); err != nil {
		t.Fatal(err)
	}
	check(nil)
	// A different service of the same application is not proven retired by
	// this endpoint's receipt, so its legacy source use must remain.
	if _, err := tx.Exec(`INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,app_protocol,status,created_at,updated_at)
		SELECT 'source-union-other-service',application_id,site_id,'other-entry',protocol,444,444,'100.64.0.61:444',source,app_protocol,status,created_at,updated_at
		FROM services WHERE id='snapshot-shared-service'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE landing_client_grants SET service_id='source-union-other-service'`); err != nil {
		t.Fatal(err)
	}
	check(legacySource)
}

func TestMeridianDerivedLandingSourceErrorPreservesReceiptAndContinuesOtherTargets(t *testing.T) {
	store, entryID, brokenEgressID := openMeridianLandingSourcesFixture(t)
	ctx := context.Background()
	healthy := enrollOrchestrationNode(t, store, "source-union-independent-egress", NodeCapabilities{Docker: true},
		[]networking.Candidate{{Address: "100.64.0.64", Interface: "tailscale0", Kind: networking.KindHeadscale}},
		networking.Profile{ServiceAddress: "100.64.0.64", HeadscaleAddress: "100.64.0.64", EnabledKinds: []string{networking.KindHeadscale}})
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	server := landing.ServerState{NodeID: healthy.ID, Revision: 1, Plan: &landing.ServerPlan{Revision: 1, Address: "100.64.0.64"}}
	serverJSON, _ := json.Marshal(server)
	if _, err := tx.Exec(`INSERT INTO landing_server_states(node_id,desired_revision,applied_revision,desired_json,applied_json,status,updated_at)
		VALUES(?,1,1,?,?,'ready',?)`, healthy.ID, serverJSON, serverJSON, stamp); err != nil {
		t.Fatal(err)
	}
	credentialID, err := store.insertMeridianCredential(ctx, tx, sharedSnapshotAccountB, sharedSnapshotEndpointID, "snapshot-shared-app", meridian.RouteCredential, healthy.ID, true, stamp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO meridian_route_grants(id,account_id,endpoint_id,egress_node_id,base_credential_id,route_credential_id,enabled,status,created_at,updated_at)
		VALUES('independent-source-grant',?,?,?,?,?,1,'pending',?,?)`, sharedSnapshotAccountB, sharedSnapshotEndpointID, healthy.ID, sharedSnapshotAccountB+"-native", credentialID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE landing_server_states SET desired_json='{}' WHERE node_id=?`, brokenEgressID); err != nil {
		t.Fatal(err)
	}
	if err := store.refreshClientLandingSources(ctx, tx, brokenEgressID); !errors.Is(err, errLandingSourceReconciliation) {
		t.Fatalf("explicit refresh must fail closed: %v", err)
	}
	const receiptStamp = "2026-09-22T00:00:00Z"
	if _, err := tx.Exec(`UPDATE agents SET last_seen_at=? WHERE id=?`, receiptStamp, entryID); err != nil {
		t.Fatal(err)
	}
	if err := store.reconcileClientLandingSourcesForNode(ctx, tx, entryID); err != nil {
		t.Fatalf("derived invalid target rolled back source observation: %v", err)
	}
	if got := meridianLandingSourcePlan(t, tx, healthy.ID).Plan.Sources; !slices.Equal(got, []landing.AuthorizedNode{{Address: "100.64.0.61", TCPOnly: true}}) {
		t.Fatalf("independent target did not converge: %#v", got)
	}
	var desired, applied []byte
	var revision uint64
	var status, message string
	if err := tx.QueryRow(`SELECT desired_json,applied_json,desired_revision,status,last_error FROM landing_server_states WHERE node_id=?`, brokenEgressID).Scan(&desired, &applied, &revision, &status, &message); err != nil {
		t.Fatal(err)
	}
	if string(desired) != "{}" || revision != 1 || status != "ready" || len(applied) == 0 || message != landingSourceReconciliationMessage {
		t.Fatalf("derived failure changed runtime authority: revision=%d status=%s error=%q", revision, status, message)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var observed string
	if err := store.db.QueryRow(`SELECT last_seen_at FROM agents WHERE id=?`, entryID).Scan(&observed); err != nil || observed != receiptStamp {
		t.Fatalf("source observation was not committed: observed=%q err=%v", observed, err)
	}
}

func TestMeridianDerivedLandingSourceDatabaseFailureStillPropagates(t *testing.T) {
	store, entryID, egressID := openMeridianLandingSourcesFixture(t)
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// Fail after the desired plan UPDATE so the savepoint must undo a partial
	// queue operation, rather than silently report a repairable model problem.
	if _, err := tx.Exec(`CREATE TEMP TRIGGER reject_source_policy_update BEFORE INSERT ON settings
		WHEN NEW.key='landing_policy_revision' BEGIN SELECT RAISE(ABORT,'source queue database failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.reconcileClientLandingSourcesForNode(ctx, tx, entryID); err == nil || errors.Is(err, errLandingSourceReconciliation) {
		t.Fatalf("database failure was hidden as a derived model error: %v", err)
	}
	if state := meridianLandingSourcePlan(t, tx, egressID); state.Revision != 1 || len(state.Plan.Sources) != 0 {
		t.Fatal("failed queue left a partially replaced desired plan")
	}
}
