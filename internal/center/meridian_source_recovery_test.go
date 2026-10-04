package center

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

type meridianRecoveryFixture struct {
	store                                              *Store
	entryID, egressID, grantID, adminID, session, csrf string
	peer                                               landing.PeerIdentity
	view                                               MeridianSourceRecoveryView
	input                                              MeridianSourceRecoveryInput
}

func applyRecoveryLanding(t *testing.T, f meridianRecoveryFixture) {
	t.Helper()
	tx, err := f.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	task, err := f.store.claimLandingServerTask(context.Background(), tx, f.egressID)
	if err != nil || task == nil {
		t.Fatalf("claim landing: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := f.store.completeLandingServer(context.Background(), commitProjectionOnlyForTest, f.egressID, task.Revision, task.Attempt, true, &f.peer); err != nil {
		t.Fatal(err)
	}
}

func openMeridianRecoveryFixture(t *testing.T) meridianRecoveryFixture {
	t.Helper()
	store, projection, commandID, grantID := openMeridianHealthCompletionFixture(t)
	completeMeridianHealthFixture(t, store, projection, commandID, meridianHealthResult(projection, store.now(), true))
	f := meridianRecoveryFixture{store: store, entryID: projection.agentID, egressID: projection.task.Peers[0].EgressID, grantID: grantID, peer: projection.task.Peers[0].Identity}
	var err error
	f.session, f.csrf, err = store.CreateFirstAdmin(context.Background(), "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	f.adminID, err = store.SessionAdminID(context.Background(), f.session)
	if err != nil {
		t.Fatal(err)
	}
	// A valid authenticated replacement retains the old private address.
	if _, err := store.db.Exec(`UPDATE agent_private_peer_capabilities SET peer_json=json_set(peer_json,'$.id','replacement-entry','$.publicKey','nodekey:replacement-entry') WHERE node_id=?`, f.entryID); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.reconcileClientLandingSourcesForNode(context.Background(), tx, f.entryID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	applyRecoveryLanding(t, f)
	// Native-only runtime can be healthy while the fixed route stays blocked.
	completeMeridianAuthorizationEndpoint(t, store, sharedSnapshotEndpointID)
	f.view, err = store.MeridianSourceRecovery(context.Background(), sharedSnapshotEndpointID)
	if err != nil {
		t.Fatal(err)
	}
	f.input = MeridianSourceRecoveryInput{PreviousFingerprint: f.view.PreviousFingerprint, CurrentFingerprint: f.view.CurrentFingerprint, EndpointRevision: f.view.EndpointRevision, ConfirmReplacement: true, ExecutionStopped: true}
	return f
}

func TestMeridianSourceRecoveryWaitsForLandingAndTransportReceipts(t *testing.T) {
	f := openMeridianRecoveryFixture(t)
	ctx := context.Background()
	var credentialsBefore string
	const credentialsSQL = `SELECT group_concat(id || ':' || protocol_secret_id || ':' || enabled,'|') FROM meridian_credentials ORDER BY id`
	if err := f.store.db.QueryRow(credentialsSQL).Scan(&credentialsBefore); err != nil {
		t.Fatal(err)
	}
	if f.view.PreviousAddress != f.view.CurrentAddress || f.view.PreviousFingerprint == f.view.CurrentFingerprint {
		t.Fatal("fixture must replace identity at the same address")
	}
	if err := f.store.RecoverMeridianSource(ctx, sharedSnapshotEndpointID, f.adminID, f.input); err != nil {
		t.Fatal(err)
	}
	desired, applied, status := readMeridianAuthorizationServer(t, f.store, f.egressID)
	if status != "pending" || desired.Revision <= applied.Revision || len(desired.Plan.Sources) != 1 || len(applied.Plan.Sources) != 0 {
		t.Fatal("replacement fabricated a server receipt")
	}
	checkBlocked := func() {
		t.Helper()
		var healthy, expiry int64
		if err := f.store.db.QueryRow(`SELECT runtime_healthy,health_expires_unix_ms FROM meridian_route_grants WHERE id=?`, f.grantID).Scan(&healthy, &expiry); err != nil || healthy != 0 || expiry != 0 {
			t.Fatalf("premature health: %d/%d %v", healthy, expiry, err)
		}
	}
	checkBlocked()
	completeMeridianAuthorizationEndpoint(t, f.store, sharedSnapshotEndpointID)
	checkBlocked()
	applyRecoveryLanding(t, f)
	checkBlocked()
	completeMeridianAuthorizationEndpoint(t, f.store, sharedSnapshotEndpointID)
	var healthy int
	if err := f.store.db.QueryRow(`SELECT runtime_healthy,status FROM meridian_route_grants WHERE id=?`, f.grantID).Scan(&healthy, &status); err != nil || healthy != 1 || status != "ready" {
		t.Fatalf("final route: %d/%s %v", healthy, status, err)
	}
	var credentialsAfter, audit string
	if err := f.store.db.QueryRow(credentialsSQL).Scan(&credentialsAfter); err != nil || credentialsBefore != credentialsAfter {
		t.Fatalf("credentials changed: %v", err)
	}
	if err := f.store.db.QueryRow(`SELECT message FROM task_events WHERE kind='meridian.source.recover' AND task_id=?`, sharedSnapshotEndpointID).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(audit, f.adminID) || !strings.Contains(audit, f.input.PreviousFingerprint) || !strings.Contains(audit, f.input.CurrentFingerprint) || strings.Contains(audit, "nodekey:") {
		t.Fatalf("incomplete or key-leaking audit: %s", audit)
	}
	if err := f.store.RecoverMeridianSource(ctx, sharedSnapshotEndpointID, f.adminID, f.input); err == nil {
		t.Fatal("replay accepted")
	}
}

func TestMeridianSourceRecoveryRejectsUnreviewedOrRacingEvidence(t *testing.T) {
	for _, name := range []string{"unconfirmed", "not-stopped", "wrong-admin", "old-fingerprint", "new-fingerprint", "revision", "identity-race", "stale", "future", "revoked", "unmanaged", "native-failed", "old-authorization", "entry-fence", "landing-fence"} {
		t.Run(name, func(t *testing.T) {
			f := openMeridianRecoveryFixture(t)
			var oldPin []byte
			if err := f.store.db.QueryRow(`SELECT source_peer_json FROM meridian_endpoints WHERE id=?`, sharedSnapshotEndpointID).Scan(&oldPin); err != nil {
				t.Fatal(err)
			}
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := f.store.db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "unconfirmed":
				f.input.ConfirmReplacement = false
			case "not-stopped":
				f.input.ExecutionStopped = false
			case "wrong-admin":
				f.adminID = "absent-admin"
			case "old-fingerprint":
				f.input.PreviousFingerprint = "wrong"
			case "new-fingerprint":
				f.input.CurrentFingerprint = "wrong"
			case "revision":
				f.input.EndpointRevision--
			case "identity-race":
				exec(`UPDATE agent_private_peer_capabilities SET peer_json=json_set(peer_json,'$.publicKey','nodekey:another-replacement') WHERE node_id=?`, f.entryID)
			case "stale":
				exec(`UPDATE agent_private_peer_capabilities SET observed_at=? WHERE node_id=?`, f.store.now().Add(-landingHealthFreshness-time.Second).Format(time.RFC3339Nano), f.entryID)
			case "future":
				exec(`UPDATE agent_private_peer_capabilities SET observed_at=? WHERE node_id=?`, f.store.now().Add(time.Minute).Format(time.RFC3339Nano), f.entryID)
			case "revoked":
				exec(`UPDATE agents SET credential_revoked_at=? WHERE id=?`, f.view.ObservedAt, f.entryID)
			case "unmanaged":
				exec(`UPDATE agents SET tailscale_ownership='external' WHERE id=?`, f.entryID)
			case "native-failed":
				exec(`UPDATE meridian_endpoints SET status='failed' WHERE id=?`, sharedSnapshotEndpointID)
			case "old-authorization":
				desired, applied, _ := readMeridianAuthorizationServer(t, f.store, f.egressID)
				desired.Plan.Sources = []landing.AuthorizedNode{{Address: f.view.PreviousAddress, TCPOnly: true}}
				applied.Plan.Sources = desired.Plan.Sources
				writeMeridianAuthorizationServer(t, f.store, desired, applied, "ready")
			case "entry-fence", "landing-fence":
				nodeID := f.entryID
				if name == "landing-fence" {
					nodeID = f.egressID
				}
				exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,expires_at,created_at,updated_at)
				 VALUES('source-recovery-fence',?,'previous-task','application.command',1,'previous-session','digest',X'01','unknown','started',?,?,?)`, nodeID, f.view.ObservedAt, f.view.ObservedAt, f.view.ObservedAt)
			}
			if err := f.store.RecoverMeridianSource(context.Background(), sharedSnapshotEndpointID, f.adminID, f.input); err == nil {
				t.Fatal("unsafe replacement accepted")
			}
			var pin []byte
			if err := f.store.db.QueryRow(`SELECT source_peer_json FROM meridian_endpoints WHERE id=?`, sharedSnapshotEndpointID).Scan(&pin); err != nil || !bytes.Equal(pin, oldPin) {
				t.Fatalf("rejection changed pin: %v", err)
			}
			var events int
			if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM task_events WHERE kind='meridian.source.recover'`).Scan(&events); err != nil || events != 0 {
				t.Fatalf("rejection committed audit: %d %v", events, err)
			}
		})
	}
}

func TestMeridianSourceRecoveryHTTPRequiresAdminAndCSRF(t *testing.T) {
	f := openMeridianRecoveryFixture(t)
	handler := NewServer(f.store, "", false).Handler()
	path := "/api/v1/meridian/endpoints/" + sharedSnapshotEndpointID + "/source-recovery"
	for _, test := range []struct {
		method        string
		session, csrf bool
		want          int
	}{
		{http.MethodGet, false, false, http.StatusUnauthorized},
		{http.MethodPost, false, false, http.StatusUnauthorized},
		{http.MethodPost, true, false, http.StatusUnauthorized},
		{http.MethodGet, true, false, http.StatusOK},
		{http.MethodPost, true, true, http.StatusAccepted},
	} {
		raw, _ := json.Marshal(f.input)
		r := httptest.NewRequest(test.method, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if test.session {
			r.AddCookie(&http.Cookie{Name: "vastora_session", Value: f.session})
		}
		if test.csrf {
			r.AddCookie(&http.Cookie{Name: "vastora_csrf", Value: f.csrf})
			r.Header.Set("X-CSRF-Token", f.csrf)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != test.want {
			t.Fatalf("%s auth=%t csrf=%t status=%d: %s", test.method, test.session, test.csrf, w.Code, w.Body.String())
		}
		if test.method == http.MethodGet && test.session && (w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "nodekey:")) {
			t.Fatal("unsafe recovery preview")
		}
	}
}

func TestMeridianSourceRecoveryPreservesOtherEntryAuthorization(t *testing.T) {
	f := openMeridianRecoveryFixture(t)
	store, ctx := f.store, context.Background()
	addMeridianSnapshotSecondEndpoint(t, store)
	var otherID string
	if err := store.db.QueryRow(`SELECT node_id FROM applications WHERE id='snapshot-second-app'`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	for _, query := range []string{
		`UPDATE agents SET tailscale_ownership='managed' WHERE id=?`,
		`UPDATE agent_network_profiles SET service_address='100.64.0.64',headscale_address='100.64.0.64' WHERE agent_id=?`,
	} {
		if _, err := store.db.Exec(query, otherID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`INSERT INTO agent_private_peer_capabilities(node_id,generation,peer_json,observed_at) VALUES(?,?,?,?)`, otherID, landing.ClientRuntimeGeneration, []byte(`{"id":"other-entry","publicKey":"nodekey:other-entry","address":"100.64.0.64"}`), stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMeridianRouteGrant(ctx, MeridianRouteGrantInput{AccountID: sharedSnapshotAccountB, EndpointID: "snapshot-second-endpoint", EgressNodeID: f.egressID}); err != nil {
		t.Fatal(err)
	}
	applyRecoveryLanding(t, f)
	var before int64
	if err := store.db.QueryRow(`SELECT desired_revision FROM meridian_endpoints WHERE id='snapshot-second-endpoint'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverMeridianSource(ctx, sharedSnapshotEndpointID, f.adminID, f.input); err != nil {
		t.Fatal(err)
	}
	desired, applied, _ := readMeridianAuthorizationServer(t, store, f.egressID)
	oldRaw, _ := json.Marshal(applied)
	newRaw, _ := json.Marshal(desired)
	if !meridianServerAuthorizesSource(oldRaw, f.egressID, "100.64.0.64") || !meridianServerAuthorizesSource(newRaw, f.egressID, "100.64.0.64") {
		t.Fatal("additive recovery withdrew unrelated entry authorization")
	}
	applyRecoveryLanding(t, f)
	var after int64
	if err := store.db.QueryRow(`SELECT desired_revision FROM meridian_endpoints WHERE id='snapshot-second-endpoint'`).Scan(&after); err != nil || before != after {
		t.Fatalf("recovery churned unrelated endpoint: %d -> %d %v", before, after, err)
	}
}
