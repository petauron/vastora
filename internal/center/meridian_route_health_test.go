package center

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/petauron/glivela"
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
		Receipt: glivela.AppliedReceipt{Revision: projection.task.Desired.Revision, ConfigSHA256: projection.task.Desired.ConfigSHA256, RuntimeReady: true},
		Stats:   json.RawMessage(`{}`), Source: projection.task.Source,
	}
	for _, peer := range projection.task.Peers {
		status := landing.MonitorStatus{Revision: projection.task.Desired.Revision, State: "blocked", CheckedAt: now}
		if healthy {
			status.State, status.LinkState, status.TCP, status.ExitIP = "healthy", "direct", true, "1.1.1.1"
			status.AllowedUntil = now.Add(10 * time.Second)
		}
		result.Peers = append(result.Peers, meridianruntime.PeerObservation{EgressID: peer.EgressID, Identity: peer.Identity, Status: status})
	}
	return result
}

func completeMeridianHealthFixture(t *testing.T, store *Store, projection meridianRuntimeProjection, commandID string, result meridianruntime.Result) {
	t.Helper()
	raw, err := json.Marshal(ApplicationTaskResult{MeridianRuntime: &result, Resources: mockIntegratedResources(t, store, commandID)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.completeApplicationCommand(context.Background(), commitProjectionOnlyForTest, projection.agentID, commandID, 1, true, "", raw, false); err != nil {
		t.Fatal(err)
	}
}

// Domain tests model an already-installed generic package. They do not execute
// Docker or prove resource adoption; those have separate runtime tests.
func seedMeridianAdoptedResources(t *testing.T, store *Store, appID string) {
	t.Helper()
	receipt := map[string]any{"version": 1, "applicationId": appID, "appKey": meridianAppKey, "taskId": "fixture-install", "packageVersion": "fixture-version", "packageRevision": 1, "manifestSha256": strings.Repeat("a", 64), "runtime": "docker", "state": "ready", "authorizedCapabilities": []string{}, "resources": []any{map[string]string{"kind": "container", "id": "fixture-container"}}}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO application_resources(application_id,package_revision,manifest_sha256,authorized_capabilities_json,resources_json,adoption_state,updated_at) VALUES(?,1,?,'[]',?,'ready',?) ON CONFLICT(application_id) DO NOTHING`, appID, strings.Repeat("a", 64), raw, store.now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

func mockIntegratedResources(t *testing.T, store *Store, commandID string) json.RawMessage {
	t.Helper()
	var appID, appKey string
	if err := store.db.QueryRow(`SELECT a.id,a.app_key FROM application_commands c JOIN applications a ON a.id=c.application_id WHERE c.id=?`, commandID).Scan(&appID, &appKey); err != nil {
		t.Fatal(err)
	}
	receipt := map[string]any{"version": 1, "applicationId": appID, "appKey": appKey, "taskId": "fixture-install", "packageVersion": "fixture-version", "packageRevision": 1, "manifestSha256": strings.Repeat("a", 64), "runtime": "docker", "state": "ready", "authorizedCapabilities": []string{}, "resources": []any{map[string]string{"kind": "container", "id": "fixture-container"}}}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO application_resources(application_id,package_revision,manifest_sha256,authorized_capabilities_json,resources_json,adoption_state,updated_at) VALUES(?,1,?,'[]',?,'ready',?) ON CONFLICT(application_id) DO NOTHING`, appID, strings.Repeat("a", 64), raw, store.now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT resources_json FROM application_resources WHERE application_id=?`, appID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	receipt["taskId"] = commandID
	raw, err = json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The new Glivela controller owns fresh runtime/route acceptance (its
// observation, subscription and quota tests exercise those semantics). Center
// must not interpret late legacy receipts or replay their traffic counters.
func assertRetiredMeridianResultPreservesState(t *testing.T, store *Store, projection meridianRuntimeProjection, commandID string, result meridianruntime.Result) {
	t.Helper()
	const snapshotSQL = `SELECT json_object(
 'endpoints',(SELECT json_group_array(json_array(id,application_id,service_id,desired_revision,applied_revision,runtime_healthy,status,hex(source_peer_json),private_key_secret_id,advertise_host,advertise_port)) FROM (SELECT * FROM meridian_endpoints ORDER BY id)),
 'accounts',(SELECT json_group_array(json_array(id,subscription_token_secret_id,subscription_token_sha256,desired_revision,applied_revision,enabled,total_bytes,last_reset_at,next_reset_at)) FROM (SELECT * FROM meridian_accounts ORDER BY id)),
 'credentials',(SELECT json_group_array(json_array(id,account_id,endpoint_id,protocol_secret_id,enabled)) FROM (SELECT * FROM meridian_credentials ORDER BY id)),
 'usage',(SELECT json_group_array(json_array(credential_id,observed_bytes,baseline_bytes,raw_up_bytes,raw_down_bytes,observed_at)) FROM (SELECT * FROM meridian_usage_watermarks ORDER BY credential_id)),
 'lineUsage',(SELECT json_group_array(json_array(credential_id,upload_bytes,download_bytes,started_at,observed_at)) FROM (SELECT * FROM meridian_line_usage ORDER BY credential_id)),
 'subscriptions',(SELECT json_group_array(json_array(account_id,secret_id,content_sha256,account_revision,created_at,updated_at)) FROM (SELECT * FROM meridian_subscription_snapshots ORDER BY account_id)),
 'routes',(SELECT json_group_array(json_array(id,desired_revision,applied_revision,runtime_healthy,health_expires_unix_ms,status)) FROM (SELECT * FROM meridian_route_grants ORDER BY id)),
 'commands',(SELECT json_group_array(json_array(id,state,attempt,hex(input_json),error)) FROM (SELECT * FROM application_commands ORDER BY id)),
 'secrets',(SELECT json_group_array(json_array(id,hex(sealed))) FROM (SELECT * FROM secrets ORDER BY id)),
 'events',(SELECT json_group_array(json_array(id,task_id,event,message)) FROM (SELECT * FROM task_events ORDER BY id)))`
	read := func() string {
		t.Helper()
		var value string
		if err := store.db.QueryRow(snapshotSQL).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := read()
	raw, err := json.Marshal(ApplicationTaskResult{MeridianRuntime: &result})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.completeApplicationCommand(context.Background(), commitProjectionOnlyForTest, projection.agentID, commandID, 1, true, "", raw, false); err == nil {
		t.Fatal("retired runtime receipt was interpreted")
	}
	if read() != before {
		t.Fatal("retired receipt changed preserved identity, subscription, configuration, traffic or history")
	}
}

func TestRetiredMeridianRuntimeCannotRenewHealthOrApplyRevision(t *testing.T) {
	store, projection, commandID, _ := openMeridianHealthCompletionFixture(t)
	for _, healthy := range []bool{false, true} {
		assertRetiredMeridianResultPreservesState(t, store, projection, commandID, meridianHealthResult(projection, store.now(), healthy))
	}
}

func TestMeridianCompletionRejectsMissingTransportProof(t *testing.T) {
	store, projection, commandID, _ := openMeridianHealthCompletionFixture(t)
	result := meridianHealthResult(projection, store.now(), true)
	result.Peers = nil
	assertRetiredMeridianResultPreservesState(t, store, projection, commandID, result)
}

func TestRetiredMeridianCompletionCannotReplayQuotaCounters(t *testing.T) {
	store, projection, commandID, _ := openMeridianHealthCompletionFixture(t)
	result := meridianHealthResult(projection, store.now(), true)
	for _, material := range projection.materials {
		if material.Credential.AccountID == sharedSnapshotAccountA && material.Credential.Kind == glivela.NativeCredential {
			result.Stats, _ = json.Marshal(map[string]any{"stat": []any{map[string]any{"name": "user>>>" + material.Credential.User + ">>>traffic>>>uplink", "value": 1000}}})
		}
	}
	for range 2 {
		assertRetiredMeridianResultPreservesState(t, store, projection, commandID, result)
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
