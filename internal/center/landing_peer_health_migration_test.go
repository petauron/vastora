package center

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestLandingPeerHealthMigrationIsForwardOnly(t *testing.T) {
	directory := t.TempDir()
	old := legacyMigrationStore(t, directory, 76)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.db.Query(`PRAGMA table_info(landing_proxy_states)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var position, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&position, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		found = found || name == "peer_health_json"
	}
	if err := rows.Err(); err != nil || !found {
		t.Fatalf("peer health column missing after migration: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v76-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatalf("pre-migration backup missing: %v %v", backups, err)
	}
}
