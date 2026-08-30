package center

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/secret"
	_ "modernc.org/sqlite"
)

func TestCenterDatabaseKeyBindingLifecycle(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	var bindings int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM storage_key_binding WHERE id = 1`).Scan(&bindings); err != nil || bindings != 1 {
		t.Fatalf("binding count = %d, err=%v", bindings, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatalf("reopen with original key: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	keyOnlyDirectory := t.TempDir()
	keyPath := filepath.Join(keyOnlyDirectory, "center.key")
	if _, err := secret.CreateKey(keyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(keyOnlyDirectory); err == nil || !strings.Contains(err.Error(), "root key exists without its database") {
		t.Fatalf("key without database error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(keyOnlyDirectory, "center.db")); !os.IsNotExist(err) {
		t.Fatalf("failed open created a database: %v", err)
	}
}

func TestCenterDatabaseRejectsInvalidRootKeyWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{"missing", func(t *testing.T, path string) { t.Helper(); mustRemove(t, path) }},
		{"replacement", func(t *testing.T, path string) {
			t.Helper()
			mustWritePrivate(t, path, bytes.Repeat([]byte{0x5a}, secret.KeySize))
		}},
		{"corrupt", func(t *testing.T, path string) { t.Helper(); mustWritePrivate(t, path, []byte("short")) }},
		{"unsafe-permissions", func(t *testing.T, path string) {
			t.Helper()
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			store, err := Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			databasePath := filepath.Join(directory, "center.db")
			keyPath := filepath.Join(directory, "center.key")
			before, err := os.ReadFile(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, keyPath)
			if _, err := Open(directory); err == nil {
				t.Fatal("Center accepted an invalid database/key pair")
			}
			after, err := os.ReadFile(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("failed open modified the database")
			}
			if test.name == "missing" {
				if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
					t.Fatalf("missing key was recreated: %v", err)
				}
			}
		})
	}
}

func TestCenterLegacyDatabaseBindsOnlyAfterEncryptedStateVerification(t *testing.T) {
	t.Run("correct-key", func(t *testing.T) {
		directory := t.TempDir()
		makeLegacyUnboundCenter(t, directory)
		store, err := Open(directory)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		var count, version int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM storage_key_binding WHERE id = 1`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("binding count = %d, err=%v", count, err)
		}
		if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != centerSchemaVersion {
			t.Fatalf("schema version = %d, err=%v", version, err)
		}
	})

	t.Run("wrong-key", func(t *testing.T) {
		directory := t.TempDir()
		makeLegacyUnboundCenter(t, directory)
		mustWritePrivate(t, filepath.Join(directory, "center.key"), bytes.Repeat([]byte{0xa5}, secret.KeySize))
		databasePath := filepath.Join(directory, "center.db")
		before, err := os.ReadFile(databasePath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Open(directory); err == nil || !strings.Contains(err.Error(), "verify legacy encrypted state") {
			t.Fatalf("wrong legacy key error = %v", err)
		}
		after, err := os.ReadFile(databasePath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("failed legacy open modified the database")
		}
	})
}

func TestCenterBackupRejectsChangedOrMixedRootKeys(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	keyPath := filepath.Join(directory, "center.key")
	originalKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	mustWritePrivate(t, keyPath, bytes.Repeat([]byte{0x3c}, secret.KeySize))
	if err := store.Backup(context.Background(), filepath.Join(t.TempDir(), "changed.vastora"), "password"); err == nil || !strings.Contains(err.Error(), "root key changed") {
		t.Fatalf("changed-key backup error = %v", err)
	}
	mustWritePrivate(t, keyPath, originalKey)

	wrongKey := bytes.Repeat([]byte{0xc3}, secret.KeySize)
	sealed, err := secret.Seal(wrongKey, []byte("mixed-key-secret"), []byte("official-catalog-signing-key"))
	if err != nil {
		t.Fatal(err)
	}
	now := store.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if _, err := store.db.Exec(`INSERT INTO secrets(id, sealed, created_at, updated_at) VALUES('mixed-key', ?, ?, ?); INSERT INTO settings(key, value) VALUES('official_catalog_signing_key', 'mixed-key')`, sealed, now, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Backup(context.Background(), filepath.Join(t.TempDir(), "mixed.vastora"), "password"); err == nil || !strings.Contains(err.Error(), "verify encrypted state") {
		t.Fatalf("mixed-key backup error = %v", err)
	}
}

func makeLegacyUnboundCenter(t *testing.T, directory string) {
	t.Helper()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	secretID, err := store.putSecret(context.Background(), tx, []byte("legacy-secret"), "official-catalog-signing-key")
	if err == nil {
		_, err = tx.Exec(`INSERT INTO settings(key, value) VALUES('official_catalog_signing_key', ?)`, secretID)
	}
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER secret_deliveries_delete_with_deployment;
		DROP TRIGGER secret_deliveries_delete_with_application_command;
		DROP TABLE secret_deliveries;
		DROP TABLE storage_key_binding;
		DELETE FROM goose_db_version WHERE version_id > 35;
		PRAGMA user_version = 35`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustWritePrivate(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
