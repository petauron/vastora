package center

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

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

// Construct historical versions by applying the released forward migrations.
// Individual migration tests must not relabel the current schema as an old
// version: that leaves future columns/tables in place and does not test an
// upgrade that any released Center could actually perform.
