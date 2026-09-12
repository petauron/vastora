package center

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestLandingClientMigrationStartsWithoutImplicitGrants(t *testing.T) {
	directory := t.TempDir()
	old := legacyMigrationStore(t, directory, 72)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, table := range []string{"landing_client_capabilities", "three_x_ui_client_accounts", "landing_client_grants", "landing_client_blocks"} {
		var count int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("migration granted implicit authority in %s: %d %v", table, count, err)
		}
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v72-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup missing: %v %v", backups, err)
	}
	backup, err := sql.Open("sqlite", backups[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var version int
	if err := backup.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 72 {
		t.Fatalf("backup was not pre-migration: %d %v", version, err)
	}
}

func TestLandingClientMigrationFailureDoesNotAdvanceSchema(t *testing.T) {
	directory := t.TempDir()
	old := legacyMigrationStore(t, directory, 72)
	if _, err := old.db.Exec(`CREATE TABLE landing_client_blocks(unexpected TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(directory); err == nil {
		store.Close()
		t.Fatal("migration accepted conflicting schema")
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, partial int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 72 {
		t.Fatalf("failed migration advanced schema: %d %v", version, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='landing_client_grants'`).Scan(&partial); err != nil || partial != 0 {
		t.Fatalf("failed migration left partial grants: %d %v", partial, err)
	}
}
