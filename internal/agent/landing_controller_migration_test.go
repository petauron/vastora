package agent

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestLandingControllerMigrationBacksUpBeforeJournalCreation(t *testing.T) {
	directory := t.TempDir()
	old, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`DROP TABLE landing_controller_state; PRAGMA user_version=17`); err != nil {
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
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != agentSchemaVersion {
		t.Fatalf("migration version %d: %v", version, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM landing_controller_state`).Scan(&count); err != nil || count != 0 {
		t.Fatal("migration created implicit account authority")
	}
	backups, err := filepath.Glob(filepath.Join(directory, "schema-17-backup-*", "agent.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("missing backup: %v %v", backups, err)
	}
	info, err := os.Stat(filepath.Dir(backups[0]))
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("backup directory is not private")
	}
	backup, err := sql.Open("sqlite", backups[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err := backup.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 17 {
		t.Fatal("snapshot is not the pre-migration database")
	}
}

func TestLandingControllerUninstallDoesNotTransferJournalOwnership(t *testing.T) {
	store, state, _ := landingSubscriptionTestState(t)
	ctx := context.Background()
	if err := store.saveLandingController(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveApplied(ctx, "unrelated-app"); err != nil {
		t.Fatal(err)
	}
	if current, err := store.landingController(ctx); err != nil || current == nil {
		t.Fatal("unrelated removal erased controller state", err)
	}
	if err := store.RemoveApplied(ctx, threeXUIKey); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM landing_controller_state`).Scan(&count); err != nil || count != 0 {
		t.Fatal("uninstalled controller kept credentials", err)
	}
}
