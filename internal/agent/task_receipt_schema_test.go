package agent

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestBaselineSchemaV25BacksUpWALAndPreservesArchive(t *testing.T) {
	directory := t.TempDir()
	old, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if _, err := old.db.Exec(`PRAGMA user_version = 25; PRAGMA wal_autocheckpoint = 0`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	task := DeploymentTask{ID: "pending-before-upgrade", Kind: "application.apply", Attempt: 2}
	original := []byte(`{"taskId":"pending-before-upgrade","attempt":2,"error":"saved result"}`)
	seedLegacyReceipt(t, old, task, "completed", original)
	var sealedBefore []byte
	if err := old.db.QueryRow(`SELECT sealed_completion FROM task_receipts WHERE task_id = ?`, task.ID).Scan(&sealedBefore); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(directory, "agent.db-wal")); err != nil || info.Size() == 0 {
		t.Fatalf("fixture has no committed WAL data: %v", err)
	}
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != agentSchemaVersion {
		t.Fatalf("migrated version=%d: %v", version, err)
	}
	backupPath := filepath.Join(directory, "schema-25-backup-baseline", "agent.db")
	if info, err := os.Stat(filepath.Dir(backupPath)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("backup directory must be private: %v", err)
	}
	if info, err := os.Stat(backupPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup file must be private: %v", err)
	}
	backup, err := sql.Open("sqlite", "file:"+backupPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err := backup.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != minSupportedAgentSchemaVersion {
		t.Fatalf("backup version=%d: %v", version, err)
	}
	for name, db := range map[string]*sql.DB{"backup": backup, "migrated": store.db} {
		var sealed []byte
		if err := db.QueryRow(`SELECT sealed_completion FROM task_receipts WHERE task_id = ?`, task.ID).Scan(&sealed); err != nil || !bytes.Equal(sealedBefore, sealed) {
			t.Fatalf("%s lost or rewrote committed completion: %v", name, err)
		}
	}
	item, digest, err := store.NextLegacyReceipt(ctx, "")
	if err != nil || item == nil || item.TaskID != task.ID || !bytes.Equal(item.Completion, original) {
		t.Fatalf("migrated archive lost evidence: %v", err)
	}
	if err := store.RetireLegacyReceipt(ctx, task.ID, digest); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if item, _, err := store.NextLegacyReceipt(ctx, ""); err != nil || item != nil {
		t.Fatalf("retired archive reappeared after restart: %v", err)
	}
}

func TestBaselineSchemaV25FailsClosedWhenBackupBlocked(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 25`); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Blocking the backup directory causes promotion to fail closed before user_version advances.
	if err := os.WriteFile(filepath.Join(directory, "schema-25-backup-baseline"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if opened, err := Open(directory); err == nil {
		_ = opened.Close()
		t.Fatal("Open accepted baseline promotion without a valid recovery snapshot")
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != minSupportedAgentSchemaVersion {
		t.Fatalf("failed migration changed version: %d %v", version, err)
	}
}
