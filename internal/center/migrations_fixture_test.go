package center

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// Earlier migrations must preserve queued work, while schema 109 must refuse
// to take ownership until the maintenance operator has resolved that work.
// Inspect the stopped database directly; do not bypass the production guard.
func openBeforeCatalogMaintenanceForTest(t *testing.T, directory string) *Store {
	t.Helper()
	opened, err := Open(directory)
	if err == nil {
		opened.Close()
		t.Fatal("schema 109 accepted unfinished historical work")
	}
	if !strings.Contains(err.Error(), "unfinished = 0") && !strings.Contains(err.Error(), "safe=1") {
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
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || (version < 99 || version >= 109) {
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
	if err := upgraded.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != centerSchemaVersion {
		t.Fatalf("resolved maintenance did not migrate: version=%d err=%v", version, err)
	}
}
