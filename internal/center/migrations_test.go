package center

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

type schemaColumn struct {
	Name, Type, Default string
	NotNull, PrimaryKey int
}

type schemaIndex struct {
	Name            string
	Unique, Partial int
	Columns         []string
}

type schemaTable struct {
	Columns []schemaColumn
	Indexes []schemaIndex
}

func TestFreshAndMigratedDatabasesHaveEquivalentSchema(t *testing.T) {
	fresh, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()

	migratedDirectory := t.TempDir()
	createLegacyVersion3Database(t, migratedDirectory)
	migrated, err := Open(migratedDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()

	freshShape := databaseSchemaShape(t, fresh.db)
	migratedShape := databaseSchemaShape(t, migrated.db)
	if !reflect.DeepEqual(freshShape, migratedShape) {
		t.Fatalf("fresh and migrated schema differ:\nfresh=%#v\nmigrated=%#v", freshShape, migratedShape)
	}
}

func databaseSchemaShape(t *testing.T, db *sql.DB) map[string]schemaTable {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	shape := make(map[string]schemaTable, len(tables))
	for _, tableName := range tables {
		value := schemaTable{}
		columnRows, err := db.Query(`SELECT name, type, "notnull", COALESCE(dflt_value, ''), pk FROM pragma_table_info(?) ORDER BY cid`, tableName)
		if err != nil {
			t.Fatal(err)
		}
		for columnRows.Next() {
			var column schemaColumn
			if err := columnRows.Scan(&column.Name, &column.Type, &column.NotNull, &column.Default, &column.PrimaryKey); err != nil {
				t.Fatal(err)
			}
			value.Columns = append(value.Columns, column)
		}
		if err := columnRows.Close(); err != nil {
			t.Fatal(err)
		}
		sort.Slice(value.Columns, func(i, j int) bool { return value.Columns[i].Name < value.Columns[j].Name })
		indexRows, err := db.Query(`SELECT name, "unique", partial FROM pragma_index_list(?)`, tableName)
		if err != nil {
			t.Fatal(err)
		}
		indexes := []schemaIndex{}
		for indexRows.Next() {
			var index schemaIndex
			if err := indexRows.Scan(&index.Name, &index.Unique, &index.Partial); err != nil {
				t.Fatal(err)
			}
			indexes = append(indexes, index)
		}
		if err := indexRows.Close(); err != nil {
			t.Fatal(err)
		}
		for _, index := range indexes {
			columnNames, err := db.Query(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, index.Name)
			if err != nil {
				t.Fatal(err)
			}
			for columnNames.Next() {
				var name string
				if err := columnNames.Scan(&name); err != nil {
					t.Fatal(err)
				}
				index.Columns = append(index.Columns, name)
			}
			if err := columnNames.Close(); err != nil {
				t.Fatal(err)
			}
			value.Indexes = append(value.Indexes, index)
		}
		sort.Slice(value.Indexes, func(i, j int) bool { return value.Indexes[i].Name < value.Indexes[j].Name })
		shape[tableName] = value
	}
	return shape
}

func TestOpenMigratesVersion3WithoutLosingPublicationsOrRoutes(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)

	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	version, err := sqliteSchemaVersion(ctx, store.db)
	if err != nil || version != centerSchemaVersion {
		t.Fatalf("schema version = %d, err = %v", version, err)
	}
	provider, err := newMigrationProvider(store.db)
	if err != nil {
		t.Fatal(err)
	}
	migrationVersion, err := provider.GetDBVersion(ctx)
	if err != nil || migrationVersion != centerSchemaVersion {
		t.Fatalf("migration version = %d, err = %v", migrationVersion, err)
	}
	for table, expected := range map[string]int{"publications": 1, "routes": 1} {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil || count != expected {
			t.Fatalf("%s count = %d, err = %v", table, count, err)
		}
	}
	var routePublication string
	if err := store.db.QueryRowContext(ctx, `SELECT publication_id FROM routes WHERE id = 'route-v3'`).Scan(&routePublication); err != nil || routePublication != "publication-v3" {
		t.Fatalf("route publication = %q, err = %v", routePublication, err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO publications(
		id, service_id, kind, gateway_node_id, hostname, dns_provider, status, created_at, updated_at
	) VALUES('publication-v4', 'service-v3', 'public_shared_443', 'agent-v3', 'raw.example.test', 'manual', 'pending', ?, ?)`, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("new publication kind was not accepted: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v3-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatalf("migration backups = %v, err = %v", backups, err)
	}
	info, err := os.Stat(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("migration backup mode = %v", info.Mode().Perm())
	}
	backupDB, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backupDB.Close()
	backupVersion, err := sqliteSchemaVersion(ctx, backupDB)
	if err != nil || backupVersion != schemaBaselineVersion {
		t.Fatalf("backup schema version = %d, err = %v", backupVersion, err)
	}
}

func TestVersion8MigrationKeepsShared443SNISeparateFromConnectionHostname(t *testing.T) {
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
	if _, err := provider.UpTo(ctx, 7); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO publications(id, service_id, kind, gateway_node_id, hostname, dns_provider, status, created_at, updated_at) VALUES('shared-v7', 'service-v3', 'public_shared_443', 'agent-v3', 'reality.legacy.example.test', 'manual', 'pending', ?, ?)`, now, now); err != nil {
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
	var connectHostname, sniHostname string
	if err := migrated.db.QueryRowContext(ctx, `SELECT hostname, sni_hostname FROM publications WHERE id = 'shared-v7'`).Scan(&connectHostname, &sniHostname); err != nil {
		t.Fatal(err)
	}
	if connectHostname != "reality.legacy.example.test" || sniHostname != connectHostname {
		t.Fatalf("migrated shared 443 hostname=%q sni=%q", connectHostname, sniHostname)
	}
}

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
		t.Fatal("migration did not enforce one active 3x-ui controller per Site")
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
		VALUES('rename-reality', 'application-v3', 'agent-v3', 'agent-v3', '3xui.reality.rename', '{"action":"rename","displayName":"US Oracle","inboundId":9,"targetApplicationId":"application-v3"}', 'failed', ?, ?)`, now, now); err != nil {
		t.Fatalf("REALITY rename command kind was not accepted: %v", err)
	}
}

func TestVersion15MigrationRemovesPerPublicationCertificateSecrets(t *testing.T) {
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
	if _, err := db.ExecContext(ctx, `INSERT INTO secrets(id, sealed, created_at, updated_at) VALUES('old-publication-certificate', X'0102', ?, ?)`, now, now); err != nil {
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
	var oldSecrets, publications, routes int
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM secrets WHERE id = 'old-publication-certificate'`).Scan(&oldSecrets); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE id = 'publication-v3' AND tls_enabled = 1`).Scan(&publications); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM routes WHERE id = 'route-v3'`).Scan(&routes); err != nil {
		t.Fatal(err)
	}
	if oldSecrets != 0 || publications != 1 || routes != 1 {
		t.Fatalf("migrated Site certificate state: oldSecrets=%d publications=%d routes=%d", oldSecrets, publications, routes)
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

func createLegacyVersion3Database(t *testing.T, directory string) {
	t.Helper()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	statements := []string{
		`INSERT INTO sites(id, organization_id, name, code, timezone, status, created_at, updated_at) VALUES('site-v3', '` + defaultOrganizationID + `', 'Legacy', 'legacy', 'UTC', 'active', '` + now + `', '` + now + `')`,
		`INSERT INTO agents(id, name, credential_hash, version, status, enrolled_at, last_seen_at, site_id) VALUES('agent-v3', 'Legacy Agent', X'0102', '0.1.0-alpha.1', 'active', '` + now + `', '` + now + `', 'site-v3')`,
		`INSERT INTO applications(id, name, node_id, site_id, app_key, status, created_at, updated_at) VALUES('application-v3', 'Legacy App', 'agent-v3', 'site-v3', 'legacy/app', 'running', '` + now + `', '` + now + `')`,
		`INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, status, created_at, updated_at) VALUES('service-v3', 'application-v3', 'site-v3', 'manager', 'http', 8080, 8080, '10.0.0.2:8080', 'catalog', 'ready', '` + now + `', '` + now + `')`,
		`INSERT INTO publications(id, service_id, kind, gateway_node_id, hostname, dns_provider, status, created_at, updated_at) VALUES('publication-v3', 'service-v3', 'lan_gateway', 'agent-v3', 'legacy.example.test', 'manual', 'ready', '` + now + `', '` + now + `')`,
		`INSERT INTO routes(id, publication_id, site_id, service_id, gateway_node_id, hostname, protocol, upstreams_json, status, created_at, updated_at) VALUES('route-v3', 'publication-v3', 'site-v3', 'service-v3', 'agent-v3', 'legacy.example.test', 'http', '[]', 'ready', '` + now + `', '` + now + `')`,
	}
	for _, statement := range statements {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`DROP TRIGGER application_commands_block_during_three_x_ui_migration`,
		`DROP TRIGGER deployments_block_during_three_x_ui_migration`,
		`DROP TABLE site_certificates`,
		`DROP TABLE three_x_ui_migrations`,
		`DROP TABLE three_x_ui_backups`,
		`DROP TABLE three_x_ui_nodes`,
		`DROP INDEX applications_one_three_x_ui_master_idx`,
		`ALTER TABLE applications DROP COLUMN role`,
		`ALTER TABLE services DROP COLUMN region_code`,
		`ALTER TABLE services DROP COLUMN display_name`,
		`DROP TABLE application_commands`,
		`DROP TABLE certificate_authorities`,
		`CREATE TABLE publications_v3 (
			id TEXT PRIMARY KEY, service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
			kind TEXT NOT NULL CHECK(kind IN ('lan_gateway', 'headscale_gateway', 'public_direct', 'cloudflare_tunnel')),
			gateway_node_id TEXT REFERENCES agents(id) ON DELETE RESTRICT, hostname TEXT NOT NULL,
			dns_provider TEXT NOT NULL CHECK(dns_provider IN ('manual', 'cloudflare', 'headscale')),
			dns_record_id TEXT NOT NULL DEFAULT '', tls_enabled INTEGER NOT NULL DEFAULT 0,
			desired_revision INTEGER NOT NULL DEFAULT 1, applied_revision INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL CHECK(status IN ('pending', 'applying', 'ready', 'degraded', 'failed', 'stopped')),
			last_error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			UNIQUE(service_id, kind, hostname))`,
		`INSERT INTO publications_v3(id, service_id, kind, gateway_node_id, hostname, dns_provider, dns_record_id, tls_enabled, desired_revision, applied_revision, status, last_error, created_at, updated_at)
		 SELECT id, service_id, kind, gateway_node_id, hostname, dns_provider, dns_record_id, tls_enabled, desired_revision, applied_revision, status, last_error, created_at, updated_at FROM publications`,
		`CREATE TABLE routes_v3 (
			id TEXT PRIMARY KEY, publication_id TEXT NOT NULL REFERENCES publications_v3(id) ON DELETE CASCADE,
			site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
			service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
			gateway_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			hostname TEXT NOT NULL, protocol TEXT NOT NULL CHECK(protocol IN ('http', 'https')),
			upstreams_json BLOB NOT NULL, tls_enabled INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL CHECK(status IN ('pending', 'applying', 'ready', 'failed')),
			desired_revision INTEGER NOT NULL DEFAULT 0, applied_revision INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			UNIQUE(publication_id, gateway_node_id))`,
		`INSERT INTO routes_v3 SELECT * FROM routes`,
		`DROP TABLE routes`,
		`DROP TABLE publications`,
		`ALTER TABLE publications_v3 RENAME TO publications`,
		`ALTER TABLE routes_v3 RENAME TO routes`,
		`DROP TABLE goose_db_version`,
		`PRAGMA user_version = 3`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatalf("prepare legacy schema: %v\n%s", err, statement)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO publications(
		id, service_id, kind, gateway_node_id, hostname, dns_provider, status, created_at, updated_at
	) VALUES('unsupported-v3', 'service-v3', 'public_shared_443', 'agent-v3', 'unsupported.example.test', 'manual', 'pending', ?, ?)`, now, now); err == nil {
		t.Fatal("legacy publications constraint unexpectedly accepted public_shared_443")
	}
}
