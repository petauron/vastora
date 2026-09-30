package agent

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestMeridianStateMigrationPreservesLegacyJournal(t *testing.T) {
	directory := t.TempDir()
	old, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	legacy := []byte("sealed-legacy-xray-worker-state")
	if _, err := old.db.Exec(`DELETE FROM xray_worker_state;
		INSERT INTO xray_worker_state(id,sealed_state) VALUES(1,?);
		DROP TABLE meridian_usage_state;
		DROP TABLE meridian_runtime_state;
		PRAGMA user_version=20`, legacy); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version, count int
	var migrated []byte
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != agentSchemaVersion {
		t.Fatalf("migration version %d: %v", version, err)
	}
	if err := store.db.QueryRow(`SELECT sealed_state FROM xray_worker_state WHERE id=1`).Scan(&migrated); err != nil || !bytes.Equal(migrated, legacy) {
		t.Fatalf("legacy journal changed during migration: %v", err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM meridian_runtime_state`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration fabricated Meridian authority: %d %v", count, err)
	}

	backups, err := filepath.Glob(filepath.Join(directory, "schema-20-backup-*", "agent.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("missing pre-migration backup: %v %v", backups, err)
	}
	if info, err := os.Stat(filepath.Dir(backups[0])); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("backup directory must be private: %v", err)
	}
	backup, err := sql.Open("sqlite", backups[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err := backup.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 20 {
		t.Fatalf("backup version %d: %v", version, err)
	}
	if err := backup.QueryRow(`SELECT sealed_state FROM xray_worker_state WHERE id=1`).Scan(&migrated); err != nil || !bytes.Equal(migrated, legacy) {
		t.Fatalf("backup lost legacy journal: %v", err)
	}
	if err := backup.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='meridian_runtime_state'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("backup contains post-migration table: %d %v", count, err)
	}
}
