package center

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersion88PreservesSubscriptionGraphAndAddsSystemOwnership(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	_, publicationID := seedFailedMeridianSubscriptionPublication(t, store, "schema-88")
	const graphQuery = `SELECT json_object('serviceId',s.id,'applicationId',s.application_id,'endpoint',s.endpoint,
		'publicationId',p.id,'hostname',p.hostname,'routeId',r.id,'upstreams',CAST(r.upstreams_json AS TEXT))
		FROM services s JOIN publications p ON p.service_id=s.id JOIN routes r ON r.publication_id=p.id WHERE p.id=?`
	var before string
	if err := store.db.QueryRow(graphQuery, publicationID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	// Reconstruct the actual released v87 constraint, including child records.
	var schema string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='services'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	schema = strings.Replace(schema, "services (", "services_v87_fixture (", 1)
	schema = strings.Replace(schema, "'catalog', 'observed', 'system'", "'catalog', 'observed'", 1)
	for _, statement := range []string{
		`PRAGMA foreign_keys=OFF`, `PRAGMA legacy_alter_table=ON`, `BEGIN IMMEDIATE`,
		`UPDATE services SET source='catalog' WHERE source='system'`, schema,
		`INSERT INTO services_v87_fixture SELECT * FROM services`, `DROP TABLE services`,
		`ALTER TABLE services_v87_fixture RENAME TO services`, `DROP TABLE goose_db_version`,
		`ALTER TABLE meridian_route_grants DROP COLUMN health_expires_unix_ms`,
		`ALTER TABLE meridian_endpoints DROP COLUMN source_peer_json`,
		`PRAGMA user_version=87`, `COMMIT`, `PRAGMA legacy_alter_table=OFF`, `PRAGMA foreign_keys=ON`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE services SET source='system'`); err == nil {
		t.Fatal("version 87 fixture unexpectedly accepts system-owned services")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	var after string
	if err := migrated.db.QueryRow(graphQuery, publicationID).Scan(&after); err != nil || before != after {
		t.Fatalf("subscription graph changed: before=%s after=%s err=%v", before, after, err)
	}
	if _, err := migrated.db.Exec(`UPDATE services SET source='system' WHERE id=(SELECT service_id FROM publications WHERE id=?)`, publicationID); err != nil {
		t.Fatal(err)
	}
	if _, err := migrated.db.Exec(`UPDATE services SET source='invalid-owner'`); err == nil {
		t.Fatal("service ownership constraint was removed")
	}
	var foreignKeys, violations, version int
	for query, target := range map[string]*int{
		`PRAGMA foreign_keys`:                           &foreignKeys,
		`SELECT COUNT(*) FROM pragma_foreign_key_check`: &violations,
		`PRAGMA user_version`:                           &version,
	} {
		if err := migrated.db.QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if foreignKeys != 1 || violations != 0 || version != centerSchemaVersion {
		t.Fatalf("migration invariants: foreign_keys=%d violations=%d version=%d", foreignKeys, violations, version)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v87-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected one pre-migration backup: %v %v", backups, err)
	}
	backup, err := sql.Open("sqlite", "file:"+backups[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err := backup.QueryRow(graphQuery, publicationID).Scan(&after); err != nil || after != before {
		t.Fatalf("backup did not preserve subscription graph: %v", err)
	}
	if err := backup.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 87 {
		t.Fatalf("backup version=%d err=%v", version, err)
	}
}
