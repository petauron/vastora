package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run only on an isolated, keyless copy of a historical Center database. This
// exercises the production migration path without opening a Center server or
// contacting an Agent. It cannot establish no-restart adoption by itself.
func TestCatalogV4HistoricalCopyRehearsal(t *testing.T) {
	directory := os.Getenv("VASTORA_CATALOG_REHEARSAL_DIR")
	if directory == "" {
		t.Skip("set VASTORA_CATALOG_REHEARSAL_DIR to an isolated historical copy")
	}
	if !strings.HasPrefix(filepath.Base(directory), "vastora-catalog-rehearsal.") {
		t.Fatal("rehearsal directory must use the dedicated temporary prefix")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("rehearsal directory must be a private real directory: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(directory, "center.key")); !os.IsNotExist(err) {
		t.Fatal("rehearsal copy must not contain the production Center key")
	}
	databasePath := filepath.Join(directory, "center.db")
	info, err = os.Lstat(databasePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("rehearsal database must be a private regular file: %v", err)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	ctx := context.Background()
	if version, err := sqliteSchemaVersion(ctx, db); err != nil || version != 100 {
		t.Fatalf("expected an original schema 100 copy, got %d: %v", version, err)
	}
	rehearsalCheckIntegrity(t, db)
	for _, table := range []string{"deployments", "application_commands"} {
		var unfinished int
		query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE state IN ('pending','running') OR reconciliation_required=1", table)
		if err := db.QueryRowContext(ctx, query).Scan(&unfinished); err != nil || unfinished != 0 {
			t.Fatalf("%s has %d unfinished or uncertain rows: %v", table, unfinished, err)
		}
	}
	var applicationCount, cacheCount, historyCount int
	for _, item := range []struct {
		query string
		out   *int
	}{
		{"SELECT COUNT(*) FROM applications", &applicationCount},
		{"SELECT COUNT(*) FROM catalog_cache", &cacheCount},
		{"SELECT COUNT(*) FROM catalog_manifest_history", &historyCount},
	} {
		if err := db.QueryRowContext(ctx, item.query).Scan(item.out); err != nil {
			t.Fatal(err)
		}
	}
	// Compare the original columns, including opaque encrypted blobs, without
	// logging their contents. Version 101 appends deployment columns by design.
	tables := []string{"applications", "deployments", "application_commands", "agents", "services", "secrets", "official_catalog_trust", "catalog_sources", "task_executions"}
	columns := make(map[string][]string, len(tables))
	before := make(map[string]string, len(tables))
	for _, table := range tables {
		columns[table] = rehearsalColumns(t, db, table)
		if table == "catalog_sources" {
			// The migration intentionally invalidates the old executable cache.
			stable := columns[table][:0]
			for _, column := range columns[table] {
				if column != "last_error" && column != "last_checked_at" {
					stable = append(stable, column)
				}
			}
			columns[table] = stable
		}
		before[table] = rehearsalDigest(t, db, rehearsalSelect(table, columns[table]))
	}
	legacyEvidence := rehearsalDigest(t, db, `SELECT cache.source_id,cache.envelope,source.public_key FROM catalog_cache cache JOIN catalog_sources source ON source.id=cache.source_id ORDER BY cache.source_id`)
	legacyHistory := rehearsalDigest(t, db, `SELECT source_id,app_id,version,manifest_sha256,first_seen_at FROM catalog_manifest_history ORDER BY source_id,app_id,version`)
	store := &Store{db: db, dataDir: directory, now: time.Now}
	if err := store.migrateSchema(ctx); err != nil {
		t.Fatalf("historical copy migration failed; preserve the copy for diagnosis: %v", err)
	}
	if version, err := sqliteSchemaVersion(ctx, db); err != nil || version != 101 {
		t.Fatalf("migrated version=%d err=%v", version, err)
	}
	for _, table := range tables {
		if after := rehearsalDigest(t, db, rehearsalSelect(table, columns[table])); after != before[table] {
			t.Errorf("migration changed historical %s rows", table)
		}
	}
	if after := rehearsalDigest(t, db, `SELECT source_id,payload,public_key FROM catalog_legacy_evidence ORDER BY source_id`); after != legacyEvidence {
		t.Error("legacy signed catalog evidence was not preserved byte-for-byte")
	}
	if after := rehearsalDigest(t, db, `SELECT source_id,app_id,version,manifest_sha256,first_seen_at FROM catalog_manifest_history ORDER BY source_id,app_id,version`); after != legacyHistory {
		t.Error("historical catalog identity or digest changed")
	}
	for _, item := range []struct {
		name  string
		query string
		want  int
	}{
		{"pending adoption records", "SELECT COUNT(*) FROM application_resources WHERE adoption_state='pending' AND resources_json='{}'", applicationCount},
		{"legacy evidence", "SELECT COUNT(*) FROM catalog_legacy_evidence", cacheCount},
		{"historical package revisions", "SELECT COUNT(*) FROM catalog_manifest_history WHERE package_revision=0", historyCount},
		{"executable legacy cache", "SELECT COUNT(*) FROM catalog_cache", 0},
		{"foreign-key violations", "SELECT COUNT(*) FROM pragma_foreign_key_check", 0},
	} {
		var got int
		if err := db.QueryRowContext(ctx, item.query).Scan(&got); err != nil || got != item.want {
			t.Errorf("%s=%d, want %d: %v", item.name, got, item.want, err)
		}
	}
	rehearsalCheckIntegrity(t, db)
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", "center-v100-before-v101-*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected one pre-migration backup, found %d: %v", len(backups), err)
	}
	t.Logf("historical copy migrated 100→101; applications=%d, legacy cache=%d, manifest identities=%d; historical rows unchanged", applicationCount, cacheCount, historyCount)
}

func rehearsalCheckIntegrity(t *testing.T, db *sql.DB) {
	t.Helper()
	var result string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&result); err != nil || result != "ok" {
		t.Fatalf("database quick_check=%q: %v", result, err)
	}
}

func rehearsalColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query("SELECT name FROM pragma_table_info(?) ORDER BY cid", table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil || len(columns) == 0 {
		t.Fatalf("inspect %s columns: %v", table, err)
	}
	return columns
}

func rehearsalSelect(table string, columns []string) string {
	quoted := make([]string, len(columns))
	for index, column := range columns {
		quoted[index] = `"` + strings.ReplaceAll(column, `"`, `""`) + `"`
	}
	return "SELECT " + strings.Join(quoted, ",") + " FROM " + table + " ORDER BY rowid"
}

func rehearsalDigest(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		if err := encoder.Encode(values); err != nil {
			t.Fatal(err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(digest.Sum(nil))
}
