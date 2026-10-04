package center

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// Opt-in rehearsal uses an online-backup snapshot, never a running database.
// The supplied file is read-only and all migration writes stay in t.TempDir.
func TestCatalogV4MigrationSnapshot(t *testing.T) {
	source := os.Getenv("VASTORA_CATALOG_MIGRATION_SNAPSHOT")
	if source == "" {
		t.Skip("requires a private database snapshot")
	}
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	directory := t.TempDir()
	target := filepath.Join(directory, "center.db")
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy snapshot: %v %v", copyErr, closeErr)
	}
	db, err := sql.Open("sqlite", target)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	version, err := sqliteSchemaVersion(ctx, db)
	if err != nil || version != 108 {
		t.Fatalf("expected released schema 108, got %d: %v", version, err)
	}
	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM applications`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	store := &Store{db: db, dataDir: directory}
	if err := store.initializeSchema(ctx, true); err != nil {
		t.Fatal(err)
	}
	var after, pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM applications`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM application_resources WHERE adoption_state='pending'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if before != after || after != pending {
		t.Fatalf("application preservation mismatch: %d %d %d", before, after, pending)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", "center-v108-before-v109-*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup not created: %v", err)
	}
	t.Logf("schema 108 to 109 verified; %d application records retained pending explicit adoption", after)
}
