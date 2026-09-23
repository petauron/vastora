package center

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestVersion91PreservesMeridianStateAndBacksUpBeforeAddingEntryQuota(t *testing.T) {
	directory := t.TempDir()
	legacy := legacyMigrationStore(t, directory, 90)
	seedMeridianVersion88HealthFixture(t, legacy.db)
	before := meridianVersion88PreservedState(t, legacy.db)
	if err := legacy.db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	if after := meridianVersion88PreservedState(t, migrated.db); !reflect.DeepEqual(before, after) {
		t.Fatal("entry quota migration changed existing Meridian identity, usage, or runtime state")
	}
	var total, used int64
	var applied, resetDay int
	var nextReset, lastReset string
	if err := migrated.db.QueryRow(`SELECT total_bytes,used_bytes,quota_applied_enabled,reset_day,next_reset_at,last_reset_at
		FROM meridian_endpoints WHERE id='v88-health-endpoint'`).Scan(&total, &used, &applied, &resetDay, &nextReset, &lastReset); err != nil || total != 0 || used != 0 || applied != 1 || resetDay != 0 || nextReset != "" || lastReset != "" {
		t.Fatalf("entry quota defaults total=%d used=%d applied=%d day=%d next=%q last=%q err=%v", total, used, applied, resetDay, nextReset, lastReset, err)
	}
	for _, statement := range []string{
		`UPDATE meridian_endpoints SET total_bytes=-1 WHERE id='v88-health-endpoint'`,
		`UPDATE meridian_endpoints SET used_bytes=-1 WHERE id='v88-health-endpoint'`,
		`UPDATE meridian_endpoints SET quota_applied_enabled=2 WHERE id='v88-health-endpoint'`,
		`UPDATE meridian_endpoints SET reset_day=32 WHERE id='v88-health-endpoint'`,
	} {
		if _, err := migrated.db.Exec(statement); err == nil {
			t.Fatalf("entry quota schema accepted invalid policy: %s", statement)
		}
	}
	var version, violations int
	if err := migrated.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != centerSchemaVersion {
		t.Fatalf("migration version=%d err=%v", version, err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("entry quota migration foreign-key violations=%d err=%v", violations, err)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v90-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatalf("pre-migration backup count=%d err=%v", len(backups), err)
	}
	info, err := os.Stat(backups[0])
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("pre-migration backup permissions: %v", err)
	}
	backup, err := sql.Open("sqlite", "file:"+backups[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if saved := meridianVersion88PreservedState(t, backup); !reflect.DeepEqual(saved, before) {
		t.Fatal("pre-migration backup did not preserve Meridian state")
	}
	if err := backup.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 90 {
		t.Fatalf("backup version=%d err=%v", version, err)
	}
}
