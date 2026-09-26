package center

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// Earlier migrations must preserve queued work, while schema 100 must refuse
// to take ownership until the maintenance operator has resolved that work.
// Inspect the stopped database directly; do not bypass the production guard.
func openBeforeCatalogMaintenanceForTest(t *testing.T, directory string) *Store {
	t.Helper()
	opened, err := Open(directory)
	if err == nil {
		opened.Close()
		t.Fatal("schema 100 accepted unfinished historical work")
	}
	if !strings.Contains(err.Error(), "unfinished = 0") {
		t.Fatalf("unexpected maintenance blocker: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 99 {
		db.Close()
		t.Fatalf("failed maintenance changed schema %d: %v", version, err)
	}
	return &Store{db: db}
}

func finishCatalogMaintenanceFixture(t *testing.T, store *Store, directory string) {
	t.Helper()
	// These are synthetic operations with no Agent side effects. Model explicit
	// operator resolution only after the original preservation/guard assertions.
	if _, err := store.db.Exec(`UPDATE application_commands SET state='failed',reconciliation_required=0,reconciliation_requested=0,error='Fixture operator resolved historical work' WHERE state IN ('pending','running') OR reconciliation_required=1`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var version int
	if err := upgraded.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 100 {
		t.Fatalf("resolved maintenance did not migrate: version=%d err=%v", version, err)
	}
}

// Older migration tests reconstruct a historical database from a fresh store.
// Newer ownership tables, names, and cross-table triggers cannot be left in
// that input.
func removePostVersion85TablesForFixture(t *testing.T, store *Store) {
	t.Helper()
	for _, statement := range []string{
		`DROP TRIGGER deployments_block_during_package_maintenance`,
		`DROP TRIGGER deployment_updates_block_during_package_maintenance`,
		`DROP TRIGGER commands_block_during_package_maintenance`,
		`DROP TRIGGER command_updates_block_during_package_maintenance`,
		`DROP TABLE application_maintenance`,
		`DROP TABLE application_adoptions`,
		`DROP TABLE application_resources`,
		`DROP TABLE catalog_legacy_evidence`,
		`ALTER TABLE deployments DROP COLUMN package_revision`,
		`ALTER TABLE deployments DROP COLUMN manifest_sha256`,
		`ALTER TABLE deployments DROP COLUMN authorized_capabilities_json`,
		`ALTER TABLE catalog_manifest_history RENAME TO catalog_manifest_history_v4_fixture`,
		`CREATE TABLE catalog_manifest_history(source_id TEXT NOT NULL, app_id TEXT NOT NULL, version TEXT NOT NULL, manifest_sha256 TEXT NOT NULL, first_seen_at TEXT NOT NULL, PRIMARY KEY(source_id,app_id,version))`,
		`INSERT INTO catalog_manifest_history SELECT source_id,app_id,version,manifest_sha256,first_seen_at FROM catalog_manifest_history_v4_fixture`,
		`DROP TABLE catalog_manifest_history_v4_fixture`,
		`ALTER TABLE agents DROP COLUMN landing_egress_addresses_json`,
		`DROP TRIGGER application_commands_block_during_meridian_cutover`,
		`DROP TRIGGER application_command_updates_block_during_meridian_cutover`,
		`DROP TRIGGER deployments_block_during_meridian_cutover`,
		`DROP TRIGGER deployment_updates_block_during_meridian_cutover`,
		`DROP TABLE meridian_deployments`,
		`DROP TABLE meridian_subscription_snapshots`,
		`DROP TABLE meridian_usage_watermarks`,
		`DROP TABLE meridian_route_grants`,
		`DROP TABLE meridian_credentials`,
		`DROP TABLE meridian_accounts`,
		`DROP TABLE meridian_endpoints`,
		`DROP TABLE meridian_cutover`,
		`DROP TABLE xray_configuration_recoveries`,
		`ALTER TABLE agent_private_peer_capabilities RENAME TO landing_client_capabilities`,
		`ALTER TABLE landing_server_states DROP COLUMN applied_json`,
	} {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}
