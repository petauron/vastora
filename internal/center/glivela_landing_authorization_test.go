package center

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/landing"
)

func glivelaAppliedLandingFixtureJSON(t *testing.T, nodeID, address string, sources ...string) ([]byte, []byte) {
	t.Helper()
	plan := &landing.ServerPlan{Revision: 1, Address: address}
	for _, source := range sources {
		plan.Sources = append(plan.Sources, landing.AuthorizedNode{Address: source, TCPOnly: true})
	}
	state := landing.ServerState{NodeID: nodeID, Revision: 1, Plan: plan}
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	peerJSON, err := json.Marshal(landing.PeerIdentity{ID: "tailnet-" + nodeID, PublicKey: "nodekey:test-" + nodeID, Address: address})
	if err != nil {
		t.Fatal(err)
	}
	return encoded, peerJSON
}

func readGlivelaAuthorizationServer(t *testing.T, store *Store, nodeID string) (landing.ServerState, landing.ServerState, string) {
	t.Helper()
	var desiredJSON, appliedJSON []byte
	var status string
	if err := store.db.QueryRow(`SELECT desired_json,applied_json,status FROM landing_server_states WHERE node_id=?`, nodeID).Scan(&desiredJSON, &appliedJSON, &status); err != nil {
		t.Fatal(err)
	}
	var desired, applied landing.ServerState
	if err := json.Unmarshal(desiredJSON, &desired); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(appliedJSON, &applied); err != nil {
		t.Fatal(err)
	}
	return desired, applied, status
}

func writeGlivelaAuthorizationServer(t *testing.T, store *Store, desired, applied landing.ServerState, status string) {
	t.Helper()
	desiredJSON, err := json.Marshal(desired)
	if err != nil {
		t.Fatal(err)
	}
	appliedJSON, err := json.Marshal(applied)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE landing_server_states SET desired_revision=?,desired_json=?,applied_revision=?,applied_json=?,status=? WHERE node_id=?`, desired.Revision, desiredJSON, applied.Revision, appliedJSON, status, desired.NodeID); err != nil {
		t.Fatal(err)
	}
}

func TestGlivelaRuntimeUsesAppliedSourceAuthorizationWhileServerIsConverging(t *testing.T) {
	for _, status := range []string{"pending", "applying"} {
		t.Run(status, func(t *testing.T) {
			store, egressID, grantID, _ := openGlivelaRuntimeIdentityFixture(t)
			desired, applied, _ := readGlivelaAuthorizationServer(t, store, egressID)
			desired.Revision++
			desired.Plan.Revision = desired.Revision
			desired.Plan.Sources = append(desired.Plan.Sources, landing.AuthorizedNode{Address: "100.64.0.64", TCPOnly: true})
			writeGlivelaAuthorizationServer(t, store, desired, applied, status)
			routes, err := readGlivelaRuntimeIdentityRoutes(t, store, nil)
			if err != nil || len(routes.grants) != 1 || len(routes.readyGrantIDs) != 1 || routes.readyGrantIDs[0] != grantID {
				t.Fatalf("new source hid the already applied source: grants=%d ready=%v err=%v", len(routes.grants), routes.readyGrantIDs, err)
			}
		})
	}
}

func TestGlivelaUnappliedSourceBlocksOnlyFixedCredentialAndStillBuildsNativeRuntime(t *testing.T) {
	store, egressID, grantID, _ := openGlivelaRuntimeIdentityFixture(t)
	desired, applied, _ := readGlivelaAuthorizationServer(t, store, egressID)
	applied.Plan.Sources = nil
	desired.Revision++
	desired.Plan.Revision = desired.Revision
	writeGlivelaAuthorizationServer(t, store, desired, applied, "pending")
	if _, err := store.db.Exec(`UPDATE applications SET image='example.test/xray:current' WHERE id='snapshot-shared-app'`); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	projection, err := store.buildGlivelaRuntimeTask(context.Background(), tx, sharedSnapshotEndpointID, "")
	if err != nil {
		t.Fatalf("unapplied remote authorization blocked native projection: %v", err)
	}
	if len(projection.task.Peers) != 0 || projection.task.Source != nil || projection.blockedRouteGrants[grantID] == "" {
		t.Fatal("desired-only source was projected as authorized")
	}
	native := 0
	for _, material := range projection.materials {
		if material.Credential.Kind == glivela.NativeCredential && material.Credential.Enabled {
			native++
		}
		if material.Credential.Kind == glivela.RouteCredential && material.Credential.Enabled {
			t.Fatal("unauthorized fixed route credential remained active")
		}
	}
	if native != 2 {
		t.Fatalf("native accounts were disabled with a waiting egress: %d", native)
	}
}

func TestGlivelaAppliedSourceDoesNotAuthorizeStoppedOrFailedLanding(t *testing.T) {
	for _, test := range []string{"failed", "stopped", "changed-address", "stop-pending"} {
		t.Run(test, func(t *testing.T) {
			store, egressID, grantID, _ := openGlivelaRuntimeIdentityFixture(t)
			desired, applied, _ := readGlivelaAuthorizationServer(t, store, egressID)
			status := test
			switch test {
			case "changed-address":
				desired.Plan.Address, status = "100.64.0.64", "pending"
			case "stop-pending":
				desired.Plan, status = nil, "pending"
			}
			writeGlivelaAuthorizationServer(t, store, desired, applied, status)
			routes, err := readGlivelaRuntimeIdentityRoutes(t, store, nil)
			if err != nil || len(routes.grants) != 0 || routes.blockedGrants[grantID] == "" {
				t.Fatalf("unavailable server retained a fixed route: grants=%d blocked=%v err=%v", len(routes.grants), routes.blockedGrants, err)
			}
		})
	}
}

func TestGlivelaLiveAndSavedSubscriptionsUseAppliedSourceAuthorization(t *testing.T) {
	for _, test := range []struct {
		name string
		want int
	}{
		{"additive-pending", 1}, {"additive-applying", 1},
		{"source-not-applied", 0}, {"source-removed", 0}, {"stop-pending", 0}, {"failed", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, grantID, _ := openGlivelaRouteHealthReadFixture(t)
			ctx := context.Background()
			initial, err := store.GlivelaSubscription(ctx, sharedSnapshotTokenB)
			if err != nil || len(initial.Routes) != 1 {
				t.Fatalf("initial fixed route: %d %v", len(initial.Routes), err)
			}
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := store.loadGlivelaSubscriptionSnapshotInTx(ctx, tx, sharedSnapshotAccountB)
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				t.Fatal(rollbackErr)
			}
			if err != nil || len(snapshot.Routes) != 1 {
				t.Fatalf("initial applied snapshot: %d %v", len(snapshot.Routes), err)
			}
			var egressID string
			if err := store.db.QueryRow(`SELECT egress_node_id FROM meridian_route_grants WHERE id=?`, grantID).Scan(&egressID); err != nil {
				t.Fatal(err)
			}
			desired, applied, _ := readGlivelaAuthorizationServer(t, store, egressID)
			desired.Revision++
			desired.Plan.Revision = desired.Revision
			status := "pending"
			switch test.name {
			case "additive-pending", "additive-applying":
				desired.Plan.Sources = append(desired.Plan.Sources, landing.AuthorizedNode{Address: "100.64.0.64", TCPOnly: true})
				if test.name == "additive-applying" {
					status = "applying"
				}
			case "source-not-applied":
				applied.Plan.Sources = nil
			case "source-removed":
				desired.Plan.Sources = nil
			case "stop-pending":
				desired.Plan = nil
			case "failed":
				status = "failed"
			}
			writeGlivelaAuthorizationServer(t, store, desired, applied, status)
			live, err := store.GlivelaSubscription(ctx, sharedSnapshotTokenB)
			if err != nil || len(live.Entries) != 1 || len(live.Routes) != test.want {
				t.Fatalf("live subscription lost source authority: native=%d routes=%d want=%d err=%v", len(live.Entries), len(live.Routes), test.want, err)
			}
			tx, err = store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			filtered, err := store.filterGlivelaSubscriptionSnapshotRoutesInTx(ctx, tx, snapshot)
			if err != nil || len(filtered.Entries) != 1 || len(filtered.Routes) != test.want {
				t.Fatalf("saved subscription lost source authority: native=%d routes=%d want=%d err=%v", len(filtered.Entries), len(filtered.Routes), test.want, err)
			}
		})
	}
}

func completeGlivelaAuthorizationEndpoint(t *testing.T, store *Store, endpointID string) {
	t.Helper()
	ctx := context.Background()
	var appID string
	if err := store.db.QueryRow(`SELECT application_id FROM meridian_endpoints WHERE id=?`, endpointID).Scan(&appID); err != nil {
		t.Fatal(err)
	}
	seedGlivelaAdoptedResources(t, store, appID)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	commandID, err := store.queueGlivelaRuntime(ctx, tx, endpointID, false)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := store.buildGlivelaRuntimeTask(ctx, tx, endpointID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state='running',attempt=1 WHERE id=?`, commandID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedHistoricalGlivelaHealthFixture(t, store, projection, commandID, glivelaHealthResult(projection, store.now(), true))
}

func TestRetiredGlivelaSourceRevocationCannotReplayArchivedRuntime(t *testing.T) {
	store, projection, commandID, grantID := openGlivelaHealthCompletionFixture(t)
	seedHistoricalGlivelaHealthFixture(t, store, projection, commandID, glivelaHealthResult(projection, store.now(), true))
	if err := store.RevokeGlivelaRouteGrant(context.Background(), grantID); err != nil {
		t.Fatal(err)
	}
	// Model a pre-handover attempt still in flight against the retained snapshot.
	// Revocation itself must not be mistaken for a newly dispatched proxy task.
	if _, err := store.db.Exec(`UPDATE application_commands SET state='running',attempt=1 WHERE id=?`, commandID); err != nil {
		t.Fatal(err)
	}
	assertRetiredProxyCommandChannel(t, store, projection.agentID)
}

func TestGlivelaQuotaDoesNotRevokeSharedTransportAuthorization(t *testing.T) {
	store, egressID, grantID, _ := openGlivelaRuntimeIdentityFixture(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_usage_watermarks SET observed_bytes=2000 WHERE credential_id=?`, sharedSnapshotAccountA+"-native"); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := store.refreshClientLandingSources(ctx, tx, egressID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	desired, applied, status := readGlivelaAuthorizationServer(t, store, egressID)
	if desired.Plan == nil || applied.Plan == nil || len(desired.Plan.Sources) != 1 || len(applied.Plan.Sources) != 1 || status != "ready" || desired.Revision != applied.Revision {
		t.Fatal("quota exhaustion incorrectly withdrew the node's transport authorization")
	}
	routes, err := readGlivelaRuntimeIdentityRoutes(t, store, nil)
	if err != nil || len(routes.grants) != 0 || routes.blockedGrants[grantID] != glivelaRouteAccessBlocked {
		t.Fatalf("account quota did not independently block its credential: %#v %v", routes, err)
	}
}

func TestGlivelaSamePeerServerReceiptWakesOnlyNewlyAuthorizedEntry(t *testing.T) {
	store, egressID, _, peer := openGlivelaRuntimeIdentityFixture(t)
	addGlivelaSnapshotSecondEndpoint(t, store)
	ctx := context.Background()
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	var entryID string
	if err := store.db.QueryRow(`SELECT node_id FROM applications WHERE id='snapshot-second-app'`).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agents SET tailscale_ownership='managed',last_seen_at=? WHERE id=?`, stamp, entryID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agent_network_profiles SET service_address='100.64.0.64',headscale_address='100.64.0.64' WHERE agent_id=?`, entryID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO agent_private_peer_capabilities(node_id,generation,peer_json,observed_at) VALUES(?,?,?,?)`, entryID, landing.ClientRuntimeGeneration,
		[]byte(`{"id":"tailnet-second-entry","publicKey":"nodekey:test-second-entry","address":"100.64.0.64"}`), stamp); err != nil {
		t.Fatal(err)
	}
	grant, err := store.CreateGlivelaRouteGrant(ctx, GlivelaRouteGrantInput{AccountID: sharedSnapshotAccountB, EndpointID: "snapshot-second-endpoint", EgressNodeID: egressID})
	if err != nil {
		t.Fatal(err)
	}
	desired, applied, status := readGlivelaAuthorizationServer(t, store, egressID)
	if status != "pending" || desired.Revision <= applied.Revision || desired.Plan == nil || len(desired.Plan.Sources) != 2 || applied.Plan == nil || len(applied.Plan.Sources) != 1 {
		t.Fatal("new grant did not atomically queue its TCP-only source authorization")
	}
	var previousCurrent, previousOther uint64
	if err := store.db.QueryRow(`SELECT desired_revision FROM meridian_endpoints WHERE id=?`, sharedSnapshotEndpointID).Scan(&previousCurrent); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT desired_revision FROM meridian_endpoints WHERE id='snapshot-second-endpoint'`).Scan(&previousOther); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	task, err := store.claimLandingServerTask(ctx, tx, egressID)
	if err != nil || task == nil {
		t.Fatalf("new source did not create a claimable landing apply: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := store.completeLandingServer(ctx, commitProjectionOnlyForTest, egressID, task.Revision, task.Attempt, true, &peer); err != nil {
		t.Fatal(err)
	}
	var current, other uint64
	if err := store.db.QueryRow(`SELECT desired_revision FROM meridian_endpoints WHERE id=?`, sharedSnapshotEndpointID).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT desired_revision FROM meridian_endpoints WHERE id='snapshot-second-endpoint'`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if current != previousCurrent || other != previousOther+1 {
		t.Fatalf("same-peer authorization receipt churned wrong endpoints: existing %d->%d new %d->%d", previousCurrent, current, previousOther, other)
	}
	var routeStatus string
	if err := store.db.QueryRow(`SELECT status FROM meridian_route_grants WHERE id=?`, grant.ID).Scan(&routeStatus); err != nil || routeStatus != "pending" {
		t.Fatalf("authorized source was not queued for entry projection: %s %v", routeStatus, err)
	}
}

func TestRetiredGlivelaReceiptCannotRewriteSnapshotDuringUnrelatedSourceFailure(t *testing.T) {
	store, initial, initialCommand, grantID := openGlivelaHealthCompletionFixture(t)
	seedHistoricalGlivelaHealthFixture(t, store, initial, initialCommand, glivelaHealthResult(initial, store.now(), true))
	ctx := context.Background()
	egressID := initial.task.Peers[0].EgressID
	if err := store.RevokeGlivelaRouteGrant(ctx, grantID); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	commandID, err := store.queueGlivelaRuntime(ctx, tx, sharedSnapshotEndpointID, false)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := store.buildGlivelaRuntimeTask(ctx, tx, sharedSnapshotEndpointID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state='running',attempt=1 WHERE id=?`, commandID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(projection.task.Peers) != 0 || projection.task.Source != nil {
		t.Fatal("revocation fixture must remove the fixed route from the applied artifact")
	}
	var desiredBefore, appliedBefore []byte
	if err := store.db.QueryRow(`SELECT desired_json,applied_json FROM landing_server_states WHERE node_id=?`, egressID).Scan(&desiredBefore, &appliedBefore); err != nil {
		t.Fatal(err)
	}

	// Corrupt another entry's durable grant only after this valid entry's
	// artifact is queued. Explicit grant creation must reject this missing pin;
	// an asynchronous cleanup must instead preserve the successful receipt.
	addGlivelaSnapshotSecondEndpoint(t, store)
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`UPDATE agents SET tailscale_ownership='managed',last_seen_at=? WHERE id=(SELECT node_id FROM applications WHERE id='snapshot-second-app')`, stamp); err != nil {
		t.Fatal(err)
	}
	tx, err = store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var baseID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM meridian_credentials WHERE endpoint_id='snapshot-second-endpoint' AND account_id=? AND kind='native'`, sharedSnapshotAccountB).Scan(&baseID); err != nil {
		t.Fatal(err)
	}
	routeID, err := store.insertGlivelaCredential(ctx, tx, sharedSnapshotAccountB, "snapshot-second-endpoint", "snapshot-second-app", glivela.RouteCredential, egressID, true, stamp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_route_grants(id,account_id,endpoint_id,egress_node_id,base_credential_id,route_credential_id,enabled,status,created_at,updated_at)
		VALUES('invalid-source-cleanup-grant',?,'snapshot-second-endpoint',?,?,?,1,'pending',?,?)`, sharedSnapshotAccountB, egressID, baseID, routeID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	assertRetiredGlivelaResultPreservesState(t, store, projection, commandID, glivelaHealthResult(projection, store.now(), true))
}
