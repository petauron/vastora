package agent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/petauron/vastora/internal/secret"
)

func TestGlivelaRuntimeJournalMigration(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if store.glivelaConfigDir != filepath.Join(dir, "glivela") {
		t.Fatal("fresh installation must use the Glivela configuration directory")
	}
	state := glivelaLandingFixture()
	if err := store.saveGlivelaRuntimeState(ctx, state); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := secret.Seal(store.key, raw, []byte(legacyRuntimeStateAAD))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE glivela_runtime_state SET sealed_state=? WHERE id=1`, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`ALTER TABLE glivela_runtime_state RENAME TO meridian_runtime_state; PRAGMA user_version=24`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	legacyDir := filepath.Join(dir, "meridian")
	if err := os.Mkdir(legacyDir, 0700); err != nil {
		t.Fatal(err)
	}

	var migrated []byte
	for range 2 {
		store, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if store.glivelaConfigDir != legacyDir {
			t.Fatal("upgrade changed the existing Docker bind path")
		}
		var sealed []byte
		if err := store.db.QueryRow(`SELECT sealed_state FROM glivela_runtime_state WHERE id=1`).Scan(&sealed); err != nil {
			t.Fatal(err)
		}
		reopened, err := secret.Open(store.key, sealed, []byte(glivelaRuntimeStateAAD))
		if err != nil || !bytes.Equal(reopened, raw) {
			t.Fatal("migration changed the runtime journal")
		}
		if migrated != nil && !bytes.Equal(migrated, sealed) {
			t.Fatal("restart rewrote the migrated journal")
		}
		migrated = sealed
		if err := VerifyRecoveryDatabaseKey(ctx, store.db, store.key); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}

	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backupPath := filepath.Join(dir, "schema-24-backup-glivela", "agent.db")
	backup, err := sql.Open("sqlite", "file:"+backupPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err := VerifyRecoveryDatabaseKey(ctx, backup, store.key); err != nil {
		t.Fatal(err)
	}

	restoredDir := t.TempDir()
	for name, source := range map[string]string{
		"agent.db":  backupPath,
		"agent.key": filepath.Join(dir, "agent.key"),
	} {
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(restoredDir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	restored, err := Open(restoredDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restoredState, err := restored.loadGlivelaRuntimeState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restoredRaw, err := json.Marshal(restoredState)
	if err != nil || !bytes.Equal(restoredRaw, raw) {
		t.Fatal("restored backup changed the Glivela runtime journal")
	}

	damaged := bytes.Clone(migrated)
	damaged[len(damaged)-1] ^= 1
	for _, fixture := range []struct {
		name       string
		ciphertext []byte
	}{
		{name: "old context", ciphertext: legacy},
		{name: "damaged current ciphertext", ciphertext: damaged},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			if _, err := store.db.Exec(`UPDATE glivela_runtime_state SET sealed_state=? WHERE id=1`, fixture.ciphertext); err != nil {
				t.Fatal(err)
			}
			if _, err := store.loadGlivelaRuntimeState(ctx); err == nil {
				t.Fatal("upgraded Agent accepted invalid runtime state")
			}
			if err := VerifyRecoveryDatabaseKey(ctx, store.db, store.key); err == nil {
				t.Fatal("backup validation accepted invalid runtime state")
			}
		})
	}
}
