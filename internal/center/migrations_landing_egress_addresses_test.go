package center

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestLandingEgressInventoryMigrationPreservesAgentAndBacksUp(t *testing.T) {
	directory := t.TempDir()
	previous := legacyMigrationStore(t, directory, 97)
	var before string
	if err := previous.db.QueryRow(`SELECT name FROM agents WHERE id='agent-v3'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var name, addresses string
	if err := upgraded.db.QueryRow(`SELECT name,landing_egress_addresses_json FROM agents WHERE id='agent-v3'`).Scan(&name, &addresses); err != nil || name != before || addresses != "[]" {
		t.Fatalf("agent changed: %q %q %v", name, addresses, err)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v97-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatalf("migration backup missing: %v %v", backups, err)
	}
	var version int
	if err := upgraded.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != centerSchemaVersion {
		t.Fatalf("version %d %v", version, err)
	}
}
