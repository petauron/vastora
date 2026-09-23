package center

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
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
	if err := store.Backup(context.Background(), filepath.Join(t.TempDir(), "changed.vastora"), "binding-password"); err == nil || !strings.Contains(err.Error(), "root key changed") {
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
	if err := store.Backup(context.Background(), filepath.Join(t.TempDir(), "mixed.vastora"), "binding-password"); err == nil || !strings.Contains(err.Error(), "verify encrypted state") {
		t.Fatalf("mixed-key backup error = %v", err)
	}
}

func TestCenterBackupRestoresLandingAndAssistantSecrets(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "backup-secret-owner", NodeCapabilities{Docker: true},
		[]networking.Candidate{{Address: "10.0.0.80", Interface: "eth0", Kind: networking.KindLAN}},
		networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}})
	now := store.now().UTC().Format(time.RFC3339Nano)
	siteID := testSiteID(t, store)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	assistantSecret, err := store.putSecret(ctx, tx, []byte("assistant-test-key"), assistantProviderSecretData)
	if err != nil {
		t.Fatal(err)
	}
	credentialSecret, err := store.putSecret(ctx, tx, []byte("landing-test-credential"), "landing-credential:backup-grant")
	if err != nil {
		t.Fatal(err)
	}
	materialSecret, err := store.putSecret(ctx, tx, []byte("landing-test-material"), "landing-material:backup-grant")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO assistant_model_providers(id,api_url,model,api_key_secret_id,status,created_at,updated_at) VALUES(1,'https://assistant.example.test','test',?,'configured',?,?)`, []any{assistantSecret, now, now}},
		{`INSERT INTO applications(id,name,node_id,site_id,app_key,status,runtime,role,created_at,updated_at) VALUES('backup-controller','3x-ui',?,?,'vastora-official/3x-ui','running','docker','master',?,?)`, []any{node.ID, siteID, now, now}},
		{`INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,app_protocol,status,created_at,updated_at) VALUES('backup-entry','backup-controller',?,'inbound-9','tcp',30009,30009,'10.0.0.80:30009','observed','vless/tcp/reality','ready',?,?)`, []any{siteID, now, now}},
		{`INSERT INTO three_x_ui_client_accounts(id,controller_id,email,metadata_json,observed_at) VALUES('backup-parent','backup-controller','Backup Parent','{}',?)`, []any{now}},
		{`INSERT INTO landing_client_grants(id,parent_id,application_id,service_id,landing_node_id,source_peer_json,grant_json,credential_secret_id,material_secret_id,status,updated_at) VALUES('backup-grant','backup-parent','backup-controller','backup-entry',?,'{}','{}',?,?,'ready',?)`, []any{node.ID, credentialSecret, materialSecret, now}},
	} {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	const password = "backup-secret-owner-password"
	backupPath := filepath.Join(t.TempDir(), "center.vastora")
	if err := store.Backup(ctx, backupPath, password); err != nil {
		t.Fatal(err)
	}
	if err := Restore(backupPath, filepath.Join(t.TempDir(), "restored"), password); err != nil {
		t.Fatal(err)
	}
}

func makeLegacyUnboundCenter(t *testing.T, directory string) {
	t.Helper()
	// Build the released schema through its actual migration sequence rather
	// than relabeling today's schema as v35.
	store := legacyMigrationStore(t, directory, 35)
	if _, err := store.db.Exec(`DROP TABLE storage_key_binding`); err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(directory, "center.key"))
	if err != nil {
		t.Fatal(err)
	}
	store.key = key
	store.now = time.Now
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	secretID, err := store.putSecret(context.Background(), tx, []byte("legacy-secret"), "official-catalog-signing-key")
	if err == nil {
		_, err = tx.Exec(`INSERT INTO settings(key, value) VALUES('official_catalog_signing_key', ?)`, secretID)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Close(); err != nil {
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
