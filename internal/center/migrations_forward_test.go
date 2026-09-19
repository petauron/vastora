package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/secret"
)

func TestVersion11MigrationSelectsOneRunningThreeXUIControllerPerSite(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	legacy := &Store{db: db}
	if err := legacy.initializeMigrationHistory(ctx, schemaBaselineVersion); err != nil {
		t.Fatal(err)
	}
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 10); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `INSERT INTO agents(id, name, credential_hash, version, status, enrolled_at, last_seen_at, site_id) VALUES('agent-v10-worker', 'Legacy Worker', X'0304', '0.1.0', 'active', ?, ?, 'site-v3')`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, created_at, updated_at)
		VALUES('three-x-ui-failed', 'Old failed 3x-ui', 'agent-v3', 'site-v3', ?, '', 'failed', 'docker', ?, ?),
		('three-x-ui-running', 'Running 3x-ui', 'agent-v10-worker', 'site-v3', ?, '', 'running', 'docker', ?, ?)`,
		threeXUIAppKey, now.Add(-time.Hour).Format(time.RFC3339Nano), now.Add(-time.Hour).Format(time.RFC3339Nano), threeXUIAppKey, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, management, status, created_at, updated_at)
		VALUES('failed-worker-panel', 'three-x-ui-failed', 'site-v3', 'panel', 'http', 2053, 2053, '10.0.0.3:2053', 'catalog', 1, 'ready', ?, ?)`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
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
	roles := map[string]string{}
	rows, err := migrated.db.QueryContext(ctx, `SELECT id, role FROM applications WHERE app_key = ?`, threeXUIAppKey)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, role string
		if err := rows.Scan(&id, &role); err != nil {
			t.Fatal(err)
		}
		roles[id] = role
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if roles["three-x-ui-running"] != threeXUIRoleMaster || roles["three-x-ui-failed"] != threeXUIRoleWorker {
		t.Fatalf("migrated 3x-ui roles = %#v", roles)
	}
	var workerPanelStatus string
	if err := migrated.db.QueryRowContext(ctx, `SELECT status FROM services WHERE id = 'failed-worker-panel'`).Scan(&workerPanelStatus); err != nil || workerPanelStatus != "stopped" {
		t.Fatalf("legacy worker panel status=%q err=%v", workerPanelStatus, err)
	}
	if _, err := migrated.db.ExecContext(ctx, `UPDATE applications SET role = 'master', status = 'pending' WHERE id = 'three-x-ui-failed'`); err == nil {
		t.Fatal("migration did not enforce one active 3x-ui controller for the Center")
	}
	var topologyTable int
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'three_x_ui_nodes'`).Scan(&topologyTable); err != nil || topologyTable != 1 {
		t.Fatalf("3x-ui topology table count=%d err=%v", topologyTable, err)
	}
}

func TestVersion13MigrationAddsRealityDisplayNames(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	legacy := &Store{db: db}
	if err := legacy.initializeMigrationHistory(ctx, schemaBaselineVersion); err != nil {
		t.Fatal(err)
	}
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 12); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, agent_id, gateway_node_id, kind, input_json, result_json, state, created_at, updated_at)
		VALUES('legacy-reality', 'application-v3', 'agent-v3', 'agent-v3', '3xui.reality.create',
		'{"name":"MacBook","connectHostname":"reality.example.test","dnsProvider":"manual","targetApplicationId":"application-v3","targetAddress":"10.0.0.2","targetPanelPort":2053}',
		'{"inboundId":9,"name":"MacBook"}', 'succeeded', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, agent_id, gateway_node_id, kind, input_json, state, created_at, updated_at)
		VALUES('legacy-pending-reality', 'application-v3', 'agent-v3', 'agent-v3', '3xui.reality.create',
		'{"name":"Phone","connectHostname":"reality.example.test","dnsProvider":"manual","targetApplicationId":"application-v3","targetAddress":"10.0.0.2","targetPanelPort":2053}',
		'pending', ?, ?)`, now, now); err != nil {
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
	var action, displayName, clientName, resultDisplayName string
	if err := migrated.db.QueryRowContext(ctx, `SELECT json_extract(input_json, '$.action'), json_extract(input_json, '$.displayName'), json_extract(input_json, '$.clientName'), json_extract(result_json, '$.displayName') FROM application_commands WHERE id = 'legacy-reality'`).Scan(&action, &displayName, &clientName, &resultDisplayName); err != nil {
		t.Fatal(err)
	}
	if action != "create" || displayName != "MacBook" || clientName != "MacBook" || resultDisplayName != "MacBook" {
		t.Fatalf("migrated REALITY names = action %q, display %q, client %q, result %q", action, displayName, clientName, resultDisplayName)
	}
	var pendingState, pendingError string
	if err := migrated.db.QueryRowContext(ctx, `SELECT state, error FROM application_commands WHERE id = 'legacy-pending-reality'`).Scan(&pendingState, &pendingError); err != nil {
		t.Fatal(err)
	}
	if pendingState != "failed" || !strings.Contains(pendingError, "choose a region") {
		t.Fatalf("legacy pending REALITY command = state %q, error %q", pendingState, pendingError)
	}
	var serviceDisplayName, serviceRegionCode string
	if err := migrated.db.QueryRowContext(ctx, `SELECT display_name, region_code FROM services WHERE id = 'service-v3'`).Scan(&serviceDisplayName, &serviceRegionCode); err != nil || serviceDisplayName != "" || serviceRegionCode != "" {
		t.Fatalf("migrated service display name = %q, region = %q, err=%v", serviceDisplayName, serviceRegionCode, err)
	}
	if _, err := migrated.db.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, agent_id, gateway_node_id, kind, input_json, state, created_at, updated_at)
		VALUES('rename-reality', 'application-v3', 'agent-v3', 'agent-v3', '3xui.reality.rename', '{"action":"rename","displayName":"US Provider A","inboundId":9,"targetApplicationId":"application-v3"}', 'failed', ?, ?)`, now, now); err != nil {
		t.Fatalf("REALITY rename command kind was not accepted: %v", err)
	}
}

func TestVersion15MigrationHandsOffPerPublicationCertificateWithoutAGap(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	legacy := &Store{db: db}
	if err := legacy.initializeMigrationHistory(ctx, schemaBaselineVersion); err != nil {
		t.Fatal(err)
	}
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 14); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	key, err := secret.LoadOrCreateKey(filepath.Join(directory, "center.key"))
	if err != nil {
		t.Fatal(err)
	}
	encodedCertificate, _ := json.Marshal(testManagedCertificate(t, "legacy.example.test"))
	sealedCertificate, err := secret.Seal(key, encodedCertificate, []byte("publication-certificate:publication-v3"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO secrets(id, sealed, created_at, updated_at) VALUES('old-publication-certificate', ?, ?, ?)`, sealedCertificate, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE publications SET certificate_secret_id = 'old-publication-certificate', certificate_not_after = ?, tls_enabled = 1 WHERE id = 'publication-v3'`, now); err != nil {
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
	var obsoleteColumns int
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('publications') WHERE name IN ('certificate_secret_id', 'certificate_not_after')`).Scan(&obsoleteColumns); err != nil || obsoleteColumns != 0 {
		t.Fatalf("obsolete publication certificate columns=%d err=%v", obsoleteColumns, err)
	}
	var oldSecrets, publications, routes, siteCertificates int
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM secrets WHERE id = 'old-publication-certificate'`).Scan(&oldSecrets); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE id = 'publication-v3' AND tls_enabled = 1`).Scan(&publications); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM routes WHERE id = 'route-v3'`).Scan(&routes); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_certificates WHERE site_id = 'site-v3' AND status = 'ready' AND secret_id IS NOT NULL`).Scan(&siteCertificates); err != nil {
		t.Fatal(err)
	}
	stored, err := migrated.storedSiteCertificate(ctx, "site-v3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := migrated.decodeSiteCertificate(stored); err != nil {
		t.Fatalf("migrated Site certificate cannot be decoded: %v", err)
	}
	if oldSecrets != 0 || publications != 1 || routes != 1 || siteCertificates != 1 {
		t.Fatalf("migrated Site certificate state: oldSecrets=%d publications=%d routes=%d siteCertificates=%d", oldSecrets, publications, routes, siteCertificates)
	}
}

func TestVersion17MigrationFailsInFlightThreeXUICommandsClosed(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	legacy := &Store{db: db}
	if err := legacy.initializeMigrationHistory(ctx, schemaBaselineVersion); err != nil {
		t.Fatal(err)
	}
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 16); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, status, runtime, role, created_at, updated_at)
		VALUES('application-v16-second', 'Second legacy app', 'agent-v3', 'site-v3', ?, 'running', 'docker', 'worker', ?, ?)`, threeXUIAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO application_commands(
		id, application_id, agent_id, gateway_node_id, kind, input_json, state, lease_expires_at, created_at, updated_at
	) VALUES
		('v16-reality-pending', 'application-v3', 'agent-v3', 'agent-v3', '3xui.reality.create', '{}', 'pending', '', ?, ?),
		('v16-clients-running', 'application-v16-second', 'agent-v3', 'agent-v3', '3xui.clients.manage', '{}', 'running', ?, ?, ?)`, now, now, now, now, now); err != nil {
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
	rows, err := migrated.db.QueryContext(ctx, `SELECT state, lease_expires_at, error FROM application_commands WHERE id IN ('v16-reality-pending', 'v16-clients-running') ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var state, lease, message string
		if err := rows.Scan(&state, &lease, &message); err != nil {
			t.Fatal(err)
		}
		if state != "failed" || lease != "" || !strings.Contains(message, "upgraded") || !strings.Contains(message, "retry") {
			t.Fatalf("migrated in-flight command state=%q lease=%q error=%q", state, lease, message)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("migrated in-flight command count=%d", count)
	}
	var indexedColumn string
	if err := migrated.db.QueryRowContext(ctx, `SELECT name FROM pragma_index_info('application_commands_one_active_idx') ORDER BY seqno LIMIT 1`).Scan(&indexedColumn); err != nil {
		t.Fatal(err)
	}
	if indexedColumn != "agent_id" {
		t.Fatalf("migrated active command index column=%q", indexedColumn)
	}
	var updateGuard int
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = 'application_command_updates_block_during_three_x_ui_migration'`).Scan(&updateGuard); err != nil {
		t.Fatal(err)
	}
	if updateGuard != 1 {
		t.Fatalf("migrated active command update guard count=%d", updateGuard)
	}
}

func TestVersion18MigrationRejectsPreexistingDeploymentCommandRace(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	legacy := &Store{db: db}
	if err := legacy.initializeMigrationHistory(ctx, schemaBaselineVersion); err != nil {
		t.Fatal(err)
	}
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 17); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO agents(id, name, credential_hash, version, status, enrolled_at, last_seen_at, site_id)
		VALUES('agent-v17-worker', 'Legacy Worker', X'0506', '0.1.0', 'active', ?, ?, 'site-v3')`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at)
		VALUES('controller-v17', 'Legacy Controller', 'agent-v3', 'site-v3', ?, '', 'running', 'docker', 'master', ?, ?),
		('worker-v17', 'Legacy Worker', 'agent-v17-worker', 'site-v3', ?, '', 'running', 'docker', 'worker', ?, ?)`, threeXUIAppKey, now, now, threeXUIAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO deployments(id, agent_id, app_key, app_version, manifest_json, config_json, operation, state, created_at, updated_at, application_id)
		VALUES('deployment-v17-active', 'agent-v17-worker', ?, '3.6.0', '{}', '{}', 'configure', 'pending', ?, ?, 'worker-v17')`, threeXUIAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, agent_id, gateway_node_id, kind, input_json, state, created_at, updated_at)
		VALUES('application-command-v17-race', 'controller-v17', 'agent-v3', 'agent-v3', '3xui.clients.manage', '{}', 'pending', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(directory); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("migrate database from 17 to %d", centerSchemaVersion)) {
		t.Fatalf("conflicting v17 state was not rejected: %v", err)
	}
	db, err = sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	version, err := sqliteSchemaVersion(ctx, db)
	if err != nil || version != 17 {
		t.Fatalf("failed migration schema version=%d err=%v", version, err)
	}
	var v18Columns int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('deployments') WHERE name IN ('service_address', 'reconciliation_required')`).Scan(&v18Columns); err != nil || v18Columns != 0 {
		t.Fatalf("failed migration left v18 columns=%d err=%v", v18Columns, err)
	}
}

func TestOpenRejectsDatabaseFromANewerRelease(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	newerVersion := centerSchemaVersion + 1
	if _, err := store.db.Exec(`INSERT INTO goose_db_version(version_id, is_applied) VALUES(?, 1)`, newerVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, newerVersion)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(directory); err == nil || !strings.Contains(err.Error(), "not supported by this release") {
		t.Fatalf("newer database error = %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", "*.db"))
	if err != nil || len(backups) != 0 {
		t.Fatalf("rejected database unexpectedly created backups: %v, err = %v", backups, err)
	}
}

func TestFailedMigrationRollsBackSchemaAndLeavesBackup(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE publications SET kind = 'invalid-legacy-kind' WHERE id = 'publication-v3'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(directory); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("migrate database from 3 to %d", centerSchemaVersion)) {
		t.Fatalf("migration error = %v", err)
	}
	db, err = sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	version, err := sqliteSchemaVersion(context.Background(), db)
	if err != nil || version != schemaBaselineVersion {
		t.Fatalf("rolled-back schema version = %d, err = %v", version, err)
	}
	var kind string
	if err := db.QueryRow(`SELECT kind FROM publications WHERE id = 'publication-v3'`).Scan(&kind); err != nil || kind != "invalid-legacy-kind" {
		t.Fatalf("rolled-back publication kind = %q, err = %v", kind, err)
	}
	var temporaryTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('publications_v4', 'routes_v4')`).Scan(&temporaryTables); err != nil || temporaryTables != 0 {
		t.Fatalf("temporary migration tables = %d, err = %v", temporaryTables, err)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v3-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatalf("failed migration backups = %v, err = %v", backups, err)
	}
}

// Construct historical versions by applying the released forward migrations.
// Individual migration tests must not relabel the current schema as an old
// version: that leaves future columns/tables in place and does not test an
// upgrade that any released Center could actually perform.
