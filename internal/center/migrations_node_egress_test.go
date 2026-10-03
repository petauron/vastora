package center

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestVersion106DefaultsAndMigrationFailure(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		dir := t.TempDir()
		previous := legacyMigrationStore(t, dir, 105)
		if conflict {
			if _, err := previous.db.Exec(`CREATE TABLE node_egress_policies(id TEXT)`); err != nil {
				t.Fatal(err)
			}
		}
		previous.Close()
		upgraded, err := Open(dir)
		if conflict {
			if err == nil {
				upgraded.Close()
				t.Fatal("conflicting migration accepted")
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			var count int
			if err := upgraded.db.QueryRow(`SELECT count(*) FROM node_egress_policies`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("migration changed existing egress: %d %v", count, err)
			}
			upgraded.Close()
		}
		backups, err := filepath.Glob(filepath.Join(dir, "migration-backups", fmt.Sprintf("center-v105-before-v%d-*.db", centerSchemaVersion)))
		if err != nil || len(backups) != 1 {
			t.Fatalf("backup missing: %v", err)
		}
		db, err := sql.Open("sqlite", filepath.Join(dir, "center.db")+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		var version int
		if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		db.Close()
		expected := centerSchemaVersion
		if conflict {
			expected = 105
		}
		if version != expected {
			t.Fatalf("version=%d expected=%d", version, expected)
		}
	}
}
