package center

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", "center-v3-before-v4-*.db"))
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

func TestOpenRejectsDatabaseFromANewerRelease(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO goose_db_version(version_id, is_applied) VALUES(5, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 5`); err != nil {
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

	if _, err := Open(directory); err == nil || !strings.Contains(err.Error(), "migrate database from 3 to 4") {
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
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", "center-v3-before-v4-*.db"))
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
		`INSERT INTO publications_v3 SELECT * FROM publications`,
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
