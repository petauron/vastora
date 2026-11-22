package agent

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

func TestAgentDatabaseRejectsUnsupportedSchemaAndMissingBindingWithoutMutation(t *testing.T) {
	tests := []struct {
		name      string
		statement string
		wantError string
	}{
		{
			name:      "pre-baseline-schema",
			statement: `PRAGMA user_version = 24`,
			wantError: "predates supported baseline 25",
		},
		{
			name:      "future-schema",
			statement: `PRAGMA user_version = 27`,
			wantError: "cannot be upgraded by this release",
		},
		{
			name:      "missing-binding",
			statement: `DROP TABLE storage_key_binding`,
			wantError: "database key binding is missing",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			store, err := Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			key := bytes.Clone(store.key)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			databasePath := filepath.Join(directory, "agent.db")
			db, err := sql.Open("sqlite", databasePath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(test.statement); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			var busy, logFrames, checkpointedFrames int
			if err := db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			if err := VerifyRecoveryDatabaseKey(context.Background(), db, key); err == nil {
				_ = db.Close()
				t.Fatal("recovery verification accepted unsupported database")
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Open(directory); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Open error = %v, want substring %q", err, test.wantError)
			}
			after, err := os.ReadFile(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("failed open modified the database")
			}
		})
	}
}
