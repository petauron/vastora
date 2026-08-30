package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/secret"
)

func TestAppliedStatePersistsSecretsLocally(t *testing.T) {
	t.Parallel()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task := komariTestTask("https://example.invalid/komari-agent", strings.Repeat("0", 64))
	status, err := store.RecordApplied(context.Background(), AppliedInstallation{
		InstanceID: "komari-main", AppKey: task.AppKey, Version: task.Manifest.Version, Manifest: task.Manifest,
		Config: json.RawMessage(`{"timezone":"UTC"}`), Secrets: json.RawMessage(`{"apiKey":"not-in-status"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	states, err := store.ListApplied(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].ConfigHash != status.ConfigHash {
		t.Fatalf("got %#v", states)
	}
	encoded, _ := json.Marshal(states)
	if string(encoded) == "" {
		t.Fatal("unexpected status serialization")
	}
	secrets, err := store.ReadAppliedSecrets(context.Background(), "komari-main")
	if err != nil {
		t.Fatal(err)
	}
	if string(secrets) != `{"apiKey":"not-in-status"}` {
		t.Fatalf("got %s", secrets)
	}
	var sealedState []byte
	if err := store.db.QueryRow(`SELECT sealed_state FROM applied_installations WHERE instance_id = ?`, "komari-main").Scan(&sealedState); err != nil {
		t.Fatal(err)
	}
	for _, privateValue := range []string{"UTC", "not-in-status", "komari-agent"} {
		if strings.Contains(string(sealedState), privateValue) {
			t.Fatalf("sealed application state leaked %q", privateValue)
		}
	}
	restorable, err := store.RestorableInstallations(context.Background())
	if err != nil || len(restorable) != 1 || restorable[0].Manifest.ID != "komari-agent" || string(restorable[0].Secrets) != `{"apiKey":"not-in-status"}` {
		t.Fatalf("restorable state = %#v, err=%v", restorable, err)
	}
}

func TestClearingGatewayStatePreventsStaleRestore(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	state := gateway.DesiredState{Revision: 1, Listeners: []gateway.Listener{{Kind: "headscale", Address: "100.64.0.2", HTTPPort: 80, HTTPSPort: 443}}, Routes: []gateway.Route{{
		ID: "route", Hostname: "cpa.apps.example.test", Protocol: "http", ListenerKind: "headscale",
		Upstreams: []gateway.Upstream{{Address: "100.64.0.10", Port: 3000}},
	}}}
	if _, err := store.RecordGatewayState(ctx, state, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearGatewayState(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GatewayState(ctx); err == nil {
		t.Fatal("removed gateway retained last-known-good state")
	}
}

func TestGatewayCertificatesAreEncryptedAtRest(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	state := gatewayState(1, 3000)
	state.Routes[0].TLSEnabled = true
	certificate := testGatewayCertificate(t, state.Routes[0].Hostname)
	if _, err := store.RecordGatewayState(ctx, state, []gateway.Certificate{certificate}); err != nil {
		t.Fatal(err)
	}
	var desired, sealed []byte
	if err := store.db.QueryRowContext(ctx, `SELECT desired_json, sealed_certificates FROM gateway_applied_state WHERE id = 1`).Scan(&desired, &sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(desired), "PRIVATE KEY") || strings.Contains(string(sealed), "PRIVATE KEY") {
		t.Fatal("gateway private key was stored in plaintext")
	}
	restored, err := store.GatewayState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Certificates) != 1 || restored.Certificates[0].PrivateKeyPEM != certificate.PrivateKeyPEM {
		t.Fatal("encrypted gateway certificate was not restored")
	}
}

func TestAgentSchemaV2MigratesResetJournalForward(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE three_x_ui_reset_journal;
		DROP TABLE task_receipts;
		DROP TABLE agent_install_operations;
		ALTER TABLE control_plane_connection DROP COLUMN sealed_private_key;
		ALTER TABLE control_plane_connection DROP COLUMN ca_fingerprint;
		ALTER TABLE applied_installations RENAME TO applied_installations_v5_test;
		CREATE TABLE applied_installations (
			instance_id TEXT PRIMARY KEY,
			app_key TEXT NOT NULL,
			version TEXT NOT NULL,
			config_json BLOB NOT NULL,
			sealed_secrets BLOB NOT NULL,
			service_address TEXT NOT NULL,
			config_hash TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
		DROP TABLE applied_installations_v5_test;
		PRAGMA user_version = 2`); err != nil {
		store.Close()
		t.Fatal(err)
	}
	legacySecrets, err := secret.Seal(store.key, []byte(`{"token":"legacy-secret"}`), []byte("agent-instance:legacy-app"))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO applied_installations(instance_id, app_key, version, config_json, sealed_secrets, service_address, config_hash, applied_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, "legacy-app", "official/legacy", "1.0.0", []byte(`{"timezone":"UTC"}`), legacySecrets, "100.64.0.2", "hash", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != agentSchemaVersion {
		t.Fatalf("schema version = %d, err=%v", version, err)
	}
	if _, _, err := store.beginThreeXUIReset(context.Background(), threeXUIResetOperationKey("service", "2026-09-01T00:00:00Z"), "service", "2026-09-01T00:00:00Z", 1, 9, "vastora-node", 10, true); err != nil {
		t.Fatalf("migrated reset journal is unavailable: %v", err)
	}
	if _, err := store.AppliedInstallation(context.Background(), "official/legacy"); !errors.Is(err, errApplicationNotInstalled) {
		t.Fatalf("unrestorable legacy state survived migration: %v", err)
	}
}

func TestAgentSchemaV8PurgesOnlyUnrestorableLegacyState(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.RecordApplied(ctx, AppliedInstallation{InstanceID: "legacy", AppKey: "aaa/legacy", Version: "1.0.0", Config: json.RawMessage(`{}`), Secrets: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	task := komariTestTask("https://example.invalid/komari-agent", strings.Repeat("0", 64))
	if _, err := store.RecordApplied(ctx, AppliedInstallation{InstanceID: task.ID, AppKey: task.AppKey, Version: task.Manifest.Version, Manifest: task.Manifest, Config: task.Config, Secrets: task.Secrets}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE agent_install_operations; PRAGMA user_version = 7`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored, err := store.RestorableInstallations(ctx)
	if err != nil || len(restored) != 1 || restored[0].AppKey != komariKey {
		t.Fatalf("restorable applications after migration = %#v, err=%v", restored, err)
	}
}

func TestAgentSchemaV10PreservesPendingCompletionOutbox(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	task := DeploymentTask{Kind: "application.apply", ID: "schema-v10-outbox", Attempt: 1, AppKey: cpaKey, Operation: "uninstall"}
	if completion, err := store.PrepareTaskReceipt(context.Background(), task); err != nil || completion != nil {
		t.Fatalf("prepare receipt = %#v, err=%v", completion, err)
	}
	if err := store.RecordTaskCompletion(context.Background(), TaskCompletion{TaskID: task.ID, Attempt: task.Attempt, Error: "stored failure"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TABLE task_receipts_v9 (
		task_id TEXT PRIMARY KEY,
		task_kind TEXT NOT NULL,
		attempt INTEGER NOT NULL CHECK(attempt > 0),
		task_hash BLOB NOT NULL,
		state TEXT NOT NULL CHECK(state IN ('processing', 'completed', 'acknowledged', 'reconciliation_required')),
		sealed_completion BLOB,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	INSERT INTO task_receipts_v9 SELECT * FROM task_receipts;
	DROP TABLE task_receipts;
	ALTER TABLE task_receipts_v9 RENAME TO task_receipts;
	PRAGMA user_version = 9`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pending, err := store.PendingTaskCompletion(context.Background())
	if err != nil || pending == nil || pending.TaskID != task.ID || pending.Error != "stored failure" {
		t.Fatalf("migrated completion = %#v, err=%v", pending, err)
	}
}
