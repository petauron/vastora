package center

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentReconnectEnrollmentReusesOfflineIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	siteID := testSiteID(t, store)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?), (?, ?)`, agentConnectionModeSetting, "lan", agentConnectURLSetting, "https://center.example.com"); err != nil {
		t.Fatal(err)
	}
	originalEnrollment, err := store.CreateAgentEnrollment(ctx, AgentEnrollmentSpec{SiteID: siteID, Name: "DataWave 9929", CenterURL: "https://center.example.com", Gateway: true, Tunnel: true})
	if err != nil {
		t.Fatal(err)
	}
	originalKey := testAgentPublicKey(t)
	original, err := store.EnrollAgent(ctx, originalEnrollment.Token, "0.1.0-alpha.103", "linux", "amd64", originalKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO site_gateways(site_id, agent_id, created_at) VALUES(?, ?, ?)`, siteID, original.ID, clock.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO agent_network_profiles(agent_id, service_address, lan_address, enabled_kinds_json, direct_public, confirmed_at, candidate_observed_at) VALUES(?, '10.0.0.7', '10.0.0.7', '["lan"]', 0, ?, ?)`, original.ID, clock.Format(time.RFC3339Nano), clock.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, status, runtime, runtime_generation, created_at, updated_at) VALUES('preserved-app', 'Preserved app', ?, ?, 'test/preserved', 'running', 'docker', 7, ?, ?)`, original.ID, siteID, clock.Format(time.RFC3339Nano), clock.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAgentReconnectEnrollment(ctx, original.ID); err == nil || !strings.Contains(err.Error(), "disconnect") {
		t.Fatalf("connected Agent reconnect error = %v", err)
	}

	clock = clock.Add(time.Minute)
	stale, err := store.CreateAgentReconnectEnrollment(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.CreateAgentReconnectEnrollment(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnrollAgent(ctx, stale.Token, "0.1.0-alpha.104", "linux", "amd64", testAgentPublicKey(t)); err == nil || !strings.Contains(err.Error(), "token is invalid") {
		t.Fatalf("replaced reconnect command was accepted: %v", err)
	}
	if err := store.authenticateAgent(ctx, original.ID, original.Credential); err == nil {
		t.Fatal("original Agent credential remained active after reconnect was requested")
	}
	profile, err := store.AgentEnrollmentInstallProfile(ctx, current.Token)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "DataWave 9929" || !profile.Capabilities.Docker || !profile.Capabilities.Gateway || !profile.Capabilities.Tunnel || strings.Join(profile.Roles, ",") != "worker,gateway" {
		t.Fatalf("reconnect profile changed: %#v", profile)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO agent_updates(id, agent_id, target_version, state, created_at, updated_at) VALUES('superseded-update', ?, '0.1.0-alpha.104', 'pending', ?, ?)`, original.ID, clock.Format(time.RFC3339Nano), clock.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}

	replacementKey := testAgentPublicKey(t)
	replacement, err := store.EnrollAgentOperation(ctx, current.Token, "reconnect-operation-1", "0.1.0-alpha.104", "linux", "arm64", replacementKey)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID != original.ID || replacement.Credential == original.Credential || replacement.Name != "DataWave 9929" {
		t.Fatalf("replacement identity = %#v, original ID = %q", replacement, original.ID)
	}
	replayed, err := store.EnrollAgentOperation(ctx, current.Token, "reconnect-operation-1", "0.1.0-alpha.104", "linux", "arm64", replacementKey)
	if err != nil || replayed.ID != replacement.ID || replayed.Credential != replacement.Credential {
		t.Fatalf("replayed reconnect = %#v, err = %v", replayed, err)
	}
	if err := store.authenticateAgent(ctx, replacement.ID, replacement.Credential); err != nil {
		t.Fatalf("replacement credential was rejected: %v", err)
	}
	var agents, gateways, profiles, applications, applicationRuntimeGeneration int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&agents); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_gateways WHERE site_id = ? AND agent_id = ?`, siteID, original.ID).Scan(&gateways); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_network_profiles WHERE agent_id = ? AND service_address = '10.0.0.7'`, original.ID).Scan(&profiles); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(runtime_generation), -1) FROM applications WHERE id = 'preserved-app' AND node_id = ?`, original.ID).Scan(&applications, &applicationRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	if agents != 1 || gateways != 1 || profiles != 1 || applications != 1 || applicationRuntimeGeneration != 0 {
		t.Fatalf("preserved records: agents=%d gateways=%d profiles=%d applications=%d appRuntime=%d", agents, gateways, profiles, applications, applicationRuntimeGeneration)
	}
	var name, storedSiteID, architecture, credentialRevokedAt, updateState string
	var storedKey []byte
	var appliedInstallations, runtimeGeneration, remoteUpdateSupported int
	if err := store.db.QueryRowContext(ctx, `SELECT name, site_id, architecture, credential_revoked_at, x25519_public_key, applied_installations, runtime_generation, remote_update_supported FROM agents WHERE id = ?`, original.ID).Scan(&name, &storedSiteID, &architecture, &credentialRevokedAt, &storedKey, &appliedInstallations, &runtimeGeneration, &remoteUpdateSupported); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT state FROM agent_updates WHERE id = 'superseded-update'`).Scan(&updateState); err != nil {
		t.Fatal(err)
	}
	if name != "DataWave 9929" || storedSiteID != siteID || architecture != "arm64" || credentialRevokedAt != "" || !bytes.Equal(storedKey, replacementKey) || appliedInstallations != 0 || runtimeGeneration != 0 || remoteUpdateSupported != 0 || updateState != "failed" {
		t.Fatalf("reconnected Agent state changed incorrectly: name=%q site=%q arch=%q revoked=%q installs=%d runtime=%d update=%d updateState=%q", name, storedSiteID, architecture, credentialRevokedAt, appliedInstallations, runtimeGeneration, remoteUpdateSupported, updateState)
	}
}

func TestMigration59AddsAgentReconnectTargetBinding(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	siteID := testSiteID(t, store)
	if _, err := store.CreateAgentEnrollment(context.Background(), AgentEnrollmentSpec{SiteID: siteID, Name: "existing enrollment", CenterURL: "https://center.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP INDEX agent_enrollment_one_reconnect_idx;
		ALTER TABLE agent_enrollment_tokens DROP COLUMN target_agent_id;
		DELETE FROM goose_db_version WHERE version_id >= 59;
		PRAGMA user_version = 58;`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	var columns, indexes, enrollments int
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('agent_enrollment_tokens') WHERE name = 'target_agent_id'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'agent_enrollment_one_reconnect_idx'`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM agent_enrollment_tokens WHERE target_agent_id IS NULL`).Scan(&enrollments); err != nil {
		t.Fatal(err)
	}
	if columns != 1 || indexes != 1 || enrollments != 1 {
		t.Fatalf("migration result: columns=%d indexes=%d enrollments=%d", columns, indexes, enrollments)
	}
}
