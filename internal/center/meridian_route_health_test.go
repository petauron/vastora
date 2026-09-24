package center

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
)

func openMeridianHealthCompletionFixture(t *testing.T) (*Store, meridianRuntimeProjection, string, string) {
	t.Helper()
	store, _, grantID, _ := openMeridianRuntimeIdentityFixture(t)
	now := store.now().UTC().Truncate(time.Millisecond)
	store.now = func() time.Time { return now }
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `UPDATE applications SET image='example.test/xray:current' WHERE id='snapshot-shared-app'`); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	commandID, err := store.queueMeridianRuntime(ctx, tx, sharedSnapshotEndpointID, false)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := store.buildMeridianRuntimeTask(ctx, tx, sharedSnapshotEndpointID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state='running',attempt=1 WHERE id=?`, commandID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(projection.task.Peers) != 1 || projection.task.Source == nil || projection.task.Peers[0].EgressID == projection.task.Peers[0].Identity.ID {
		t.Fatal("fixture must bind the complete, distinct entry and egress identities")
	}
	return store, projection, commandID, grantID
}

func meridianHealthResult(projection meridianRuntimeProjection, now time.Time, healthy bool) meridianruntime.Result {
	result := meridianruntime.Result{
		Receipt: meridian.AppliedReceipt{Revision: projection.task.Desired.Revision, ConfigSHA256: projection.task.Desired.ConfigSHA256, RuntimeReady: true},
		Stats:   json.RawMessage(`{}`), Source: projection.task.Source,
	}
	for _, peer := range projection.task.Peers {
		status := landing.MonitorStatus{Revision: projection.task.Desired.Revision, State: "blocked", CheckedAt: now}
		if healthy {
			status.State, status.LinkState, status.TCP, status.ExitIPv4 = "healthy", "direct", true, "1.1.1.1"
			status.AllowedUntil = now.Add(10 * time.Second)
		}
		result.Peers = append(result.Peers, meridianruntime.PeerObservation{EgressID: peer.EgressID, Identity: peer.Identity, Status: status})
	}
	return result
}

func completeMeridianHealthFixture(t *testing.T, store *Store, projection meridianRuntimeProjection, commandID string, result meridianruntime.Result) {
	t.Helper()
	raw, err := json.Marshal(ApplicationTaskResult{MeridianRuntime: &result})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.completeApplicationCommand(context.Background(), commitProjectionOnlyForTest, projection.agentID, commandID, 1, true, "", raw, false); err != nil {
		t.Fatal(err)
	}
}

func TestMeridianRuntimeSuccessNeedsPeerEvidenceAndHeartbeatRenewsWithoutRevisionChange(t *testing.T) {
	store, projection, commandID, grantID := openMeridianHealthCompletionFixture(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `UPDATE services SET app_protocol='vless/tcp/reality' WHERE id='snapshot-shared-service'`); err != nil {
		t.Fatal(err)
	}
	completeMeridianHealthFixture(t, store, projection, commandID, meridianHealthResult(projection, store.now(), false))
	var protocol string
	if err := store.db.QueryRowContext(ctx, `SELECT app_protocol FROM services WHERE id='snapshot-shared-service'`).Scan(&protocol); err != nil {
		t.Fatal(err)
	}
	if protocol != meridianEntryProtocol {
		t.Fatalf("Meridian runtime receipt left legacy service protocol: %q", protocol)
	}
	check := func(wantHealthy int, wantStatus string) {
		t.Helper()
		var healthy int
		var status string
		var expiry int64
		if err := store.db.QueryRowContext(ctx, `SELECT runtime_healthy,status,health_expires_unix_ms FROM meridian_route_grants WHERE id=?`, grantID).Scan(&healthy, &status, &expiry); err != nil {
			t.Fatal(err)
		}
		if healthy != wantHealthy || status != wantStatus || (wantHealthy == 1) != (expiry > store.now().UnixMilli()) {
			t.Fatalf("route health=%d status=%s expiry=%d", healthy, status, expiry)
		}
		var desired, applied uint64
		var endpointHealthy int
		if err := store.db.QueryRowContext(ctx, `SELECT desired_revision,applied_revision,runtime_healthy FROM meridian_endpoints WHERE id=?`, sharedSnapshotEndpointID).Scan(&desired, &applied, &endpointHealthy); err != nil {
			t.Fatal(err)
		}
		if desired != projection.task.Desired.Revision || applied != desired || endpointHealthy != 1 {
			t.Fatal("remote egress health must not change the applied native runtime or churn revisions")
		}
		if native, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB); err != nil || len(native.Entries) != 1 {
			t.Fatalf("unrelated native subscription interrupted: entries=%d err=%v", len(native.Entries), err)
		}
	}
	check(0, "blocked")
	for _, healthy := range []bool{true, false, true} {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.recordMeridianRuntimeObservation(ctx, tx, projection.agentID, meridianHealthResult(projection, store.now(), healthy), store.now()); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if healthy {
			check(1, "ready")
		} else {
			check(0, "blocked")
		}
	}
}

func TestMeridianCompletionRejectsMissingTransportProof(t *testing.T) {
	store, projection, commandID, grantID := openMeridianHealthCompletionFixture(t)
	result := meridianHealthResult(projection, store.now(), true)
	result.Peers = nil
	completeMeridianHealthFixture(t, store, projection, commandID, result)
	var state string
	var healthy, expiry int64
	if err := store.db.QueryRow(`SELECT state FROM application_commands WHERE id=?`, commandID).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("incomplete proof accepted: state=%s err=%v", state, err)
	}
	if err := store.db.QueryRow(`SELECT runtime_healthy,health_expires_unix_ms FROM meridian_route_grants WHERE id=?`, grantID).Scan(&healthy, &expiry); err != nil || healthy != 0 || expiry != 0 {
		t.Fatalf("incomplete proof became healthy: health=%d expiry=%d err=%v", healthy, expiry, err)
	}
}

func TestMeridianCompletionCrossingQuotaQueuesNewDesiredRevision(t *testing.T) {
	store, projection, commandID, _ := openMeridianHealthCompletionFixture(t)
	result := meridianHealthResult(projection, store.now(), true)
	for _, material := range projection.materials {
		if material.Credential.AccountID == sharedSnapshotAccountA && material.Credential.Kind == meridian.NativeCredential {
			result.Stats, _ = json.Marshal(map[string]any{"stat": []any{map[string]any{"name": "user>>>" + material.Credential.User + ">>>traffic>>>uplink", "value": 1000}}})
		}
	}
	completeMeridianHealthFixture(t, store, projection, commandID, result)
	ctx := context.Background()
	var desired, applied uint64
	var healthy int
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision,applied_revision,runtime_healthy,status FROM meridian_endpoints WHERE id=?`, sharedSnapshotEndpointID).Scan(&desired, &applied, &healthy, &status); err != nil {
		t.Fatal(err)
	}
	if desired != projection.task.Desired.Revision+1 || applied != projection.task.Desired.Revision || healthy != 0 || status != "pending" {
		t.Fatalf("quota completion stranded old artifact: desired=%d applied=%d health=%d status=%s", desired, applied, healthy, status)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	next, err := store.buildMeridianRuntimeTask(ctx, tx, sharedSnapshotEndpointID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, material := range next.materials {
		if material.Credential.AccountID == sharedSnapshotAccountA && material.Credential.Enabled {
			t.Fatal("quota crossing did not disable the account in the next full artifact")
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if other, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB); err != nil || len(other.Entries) != 1 {
		t.Fatalf("quota crossing removed another account: entries=%d err=%v", len(other.Entries), err)
	}
}

func TestMeridianSourcePinDoesNotFollowCurrentCapabilityReplacement(t *testing.T) {
	store, _, _, _ := openMeridianRuntimeIdentityFixture(t)
	ctx := context.Background()
	var before []byte
	if err := store.db.QueryRowContext(ctx, `SELECT source_peer_json FROM meridian_endpoints WHERE id=?`, sharedSnapshotEndpointID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var source landing.PeerIdentity
	if json.Unmarshal(before, &source) != nil || source.ID == "" {
		t.Fatal("first explicit route did not pin entry identity")
	}
	source.ID = "replaced-tailnet-machine"
	replaced, _ := json.Marshal(source)
	if _, err := store.db.ExecContext(ctx, `UPDATE agent_private_peer_capabilities SET peer_json=? WHERE node_id=(SELECT node_id FROM applications WHERE id='snapshot-shared-app')`, replaced); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := store.authorizeMeridianEntrySource(ctx, tx, sharedSnapshotEndpointID); err == nil {
		t.Fatal("current identity silently replaced an existing private source authority")
	}
	var after []byte
	if err := tx.QueryRowContext(ctx, `SELECT source_peer_json FROM meridian_endpoints WHERE id=?`, sharedSnapshotEndpointID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rejected replacement changed the source pin")
	}
}
