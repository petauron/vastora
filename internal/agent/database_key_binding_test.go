package agent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/secret"
	_ "modernc.org/sqlite"
)

func TestAgentDatabaseKeyBindingLifecycle(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM storage_key_binding WHERE id = 1`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("binding count = %d, err=%v", count, err)
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
	if _, err := secret.CreateKey(filepath.Join(keyOnlyDirectory, "agent.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(keyOnlyDirectory); err == nil || !strings.Contains(err.Error(), "local key exists without its database") {
		t.Fatalf("key without database error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(keyOnlyDirectory, "agent.db")); !os.IsNotExist(err) {
		t.Fatalf("failed open created a database: %v", err)
	}
}

func TestAgentDatabaseRejectsInvalidLocalKeyWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{"missing", func(t *testing.T, path string) {
			t.Helper()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{"replacement", func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, bytes.Repeat([]byte{0x69}, secret.KeySize), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt", func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
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
			databasePath := filepath.Join(directory, "agent.db")
			keyPath := filepath.Join(directory, "agent.key")
			before, err := os.ReadFile(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, keyPath)
			if _, err := Open(directory); err == nil {
				t.Fatal("Agent accepted an invalid database/key pair")
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

func TestAgentLegacyDatabaseBindsOnlyAfterEncryptedStateVerification(t *testing.T) {
	t.Run("correct-key", func(t *testing.T) {
		directory := t.TempDir()
		makeLegacyUnboundAgent(t, directory)
		store, err := Open(directory)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		var count, version int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM storage_key_binding WHERE id = 1`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("binding count = %d, err=%v", count, err)
		}
		if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != agentSchemaVersion {
			t.Fatalf("schema version = %d, err=%v", version, err)
		}
	})

	t.Run("wrong-key", func(t *testing.T) {
		directory := t.TempDir()
		makeLegacyUnboundAgent(t, directory)
		if err := os.WriteFile(filepath.Join(directory, "agent.key"), bytes.Repeat([]byte{0x96}, secret.KeySize), 0o600); err != nil {
			t.Fatal(err)
		}
		databasePath := filepath.Join(directory, "agent.db")
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

func makeLegacyUnboundAgent(t *testing.T, directory string) {
	t.Helper()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordApplied(context.Background(), AppliedInstallation{
		InstanceID: "legacy-app", AppKey: "official/legacy", Version: "1.0.0",
		Config: json.RawMessage(`{"enabled":true}`), Secrets: json.RawMessage(`{"token":"legacy"}`),
	}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE storage_key_binding;
		ALTER TABLE task_receipts DROP COLUMN runtime_generation;
		PRAGMA user_version = 10`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
