package center

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestRecoveryMigrationsPreserveVersion61IdentityAndOwnership(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	legacy := &Store{db: db}
	if err := legacy.initializeMigrationHistory(ctx, schemaBaselineVersion); err != nil {
		t.Fatal(err)
	}
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 61); err != nil {
		t.Fatal(err)
	}
	var priorName, priorNode string
	if err := db.QueryRow(`SELECT name, node_id FROM applications WHERE id = 'application-v3'`).Scan(&priorName, &priorNode); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	var name, node, recoveryCode string
	if err := current.db.QueryRow(`SELECT app.name, app.node_id, a.runtime_recovery FROM applications app JOIN agents a ON a.id = app.node_id WHERE app.id = 'application-v3'`).Scan(&name, &node, &recoveryCode); err != nil {
		t.Fatal(err)
	}
	if name != priorName || node != priorNode || recoveryCode != "" {
		t.Fatal("migration changed persisted application identity or ownership")
	}
	for _, table := range []string{"agent_network_profile_recovery", "recovery_evidence"} {
		var count int
		if err := current.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("missing recovery table %s: %v", table, err)
		}
	}
	var foreignKeyViolation string
	if err := current.db.QueryRow(`PRAGMA foreign_key_check`).Scan(&foreignKeyViolation); err != sql.ErrNoRows {
		t.Fatalf("migration left foreign key violation: %v", err)
	}
}
