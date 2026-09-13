package agent

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// Historical fixtures constructed from Open must remove indexes introduced by
// schema 19, as well as any later tables/columns removed by the individual test.
func dropTaskReceiptIndexesForFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DROP INDEX task_receipts_pending_completion;
		DROP INDEX task_receipts_unresolved_application;
		DROP INDEX task_receipts_expiry;`); err != nil {
		t.Fatal(err)
	}
}

func TestTaskReceiptSchemaV19BacksUpWALAndPreservesArchive(t *testing.T) {
	directory := t.TempDir()
	old, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	dropTaskReceiptIndexesForFixture(t, old.db)
	if _, err := old.db.Exec(`PRAGMA user_version = 18; PRAGMA wal_autocheckpoint = 0`); err != nil {
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
	backups, err := filepath.Glob(filepath.Join(directory, "schema-18-backup-*", "agent.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("pre-migration backup: %v %v", backups, err)
	}
	if info, err := os.Stat(filepath.Dir(backups[0])); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("backup directory must be private: %v", err)
	}
	backup, err := sql.Open("sqlite", backups[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err := backup.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 18 {
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
	if backups, err := filepath.Glob(filepath.Join(directory, "schema-18-backup-*", "agent.db")); err != nil || len(backups) != 1 {
		t.Fatalf("reopen repeated migration: %v %v", backups, err)
	}
}

func TestTaskReceiptSchemaV19FailsClosed(t *testing.T) {
	for _, failure := range []string{"backup", "index"} {
		t.Run(failure, func(t *testing.T) {
			directory := t.TempDir()
			store, err := Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			dropTaskReceiptIndexesForFixture(t, store.db)
			if _, err := store.db.Exec(`PRAGMA user_version = 18`); err != nil {
				t.Fatal(err)
			}
			if failure == "backup" {
				// A file is not a valid parent for a backup directory.
				err = migrateTaskReceiptIndexesV19(store.db, filepath.Join(directory, "agent.db"))
			} else {
				// Fail on the second CREATE INDEX, after the first would succeed.
				if _, err := store.db.Exec(`CREATE INDEX task_receipts_unresolved_application ON task_receipts(state)`); err != nil {
					t.Fatal(err)
				}
				var opened *Store
				opened, err = Open(directory)
				if opened != nil {
					opened.Close()
					t.Fatal("Open accepted an incomplete migration")
				}
			}
			if err == nil {
				t.Fatal("migration ignored failure")
			}
			var version, count int
			if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 18 {
				t.Fatalf("failed migration changed version: %d %v", version, err)
			}
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name IN ('task_receipts_pending_completion', 'task_receipts_expiry')`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed migration left partial indexes: %d %v", count, err)
			}
			if failure == "index" {
				backups, err := filepath.Glob(filepath.Join(directory, "schema-18-backup-*", "agent.db"))
				if err != nil || len(backups) != 1 {
					t.Fatalf("failed migration lost backup: %v %v", backups, err)
				}
			}
		})
	}
}
