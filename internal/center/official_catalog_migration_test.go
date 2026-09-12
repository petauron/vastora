package center

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestOfficialTrustMigrationDropsOnlySupersededTrust(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	store := &Store{db: db}
	if err := store.initializeMigrationHistory(ctx, schemaBaselineVersion); err != nil {
		t.Fatal(err)
	}
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 71); err != nil {
		t.Fatal(err)
	}
	// Production Open verifies retained private caches when backfilling their
	// immutable history. Use real signed legacy envelopes, not opaque bytes.
	type sourceFixture struct {
		id, name, url, generation string
		publicKey                 ed25519.PublicKey
		envelope                  []byte
	}
	fixtures := []sourceFixture{
		{id: "vastora-official", name: "Official", url: "builtin://vastora-official", generation: "official-generation"},
		{id: "private-fixture", name: "Private", url: "https://example.invalid/catalog", generation: "private-generation"},
	}
	for index := range fixtures {
		fixture := &fixtures[index]
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		fixture.publicKey = publicKey
		fixture.envelope = signedCatalogEnvelope(t, privateKey, catalogLifecycleManifest("1.0.0", "Migration fixture"))
		if _, err := db.ExecContext(ctx, `INSERT INTO catalog_sources(id, display_name, url, public_key, enabled, refresh_seconds, generation, revision, created_at)
			VALUES(?, ?, ?, ?, 1, 3600, ?, 7, '2026-09-11T00:00:00Z')`, fixture.id, fixture.name, fixture.url, fixture.publicKey, fixture.generation); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO catalog_cache(source_id, envelope, fetched_at) VALUES(?, ?, '2026-09-11T00:00:00Z')`, fixture.id, fixture.envelope); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO catalog_manifest_history(source_id, app_id, version, manifest_sha256, first_seen_at) VALUES('vastora-official', 'pulse', '0.1.0', 'retained-history', '2026-09-11T00:00:00Z')`,
		`INSERT INTO settings(key, value) VALUES('official_catalog_signing_key', 'obsolete-local-key'), ('unrelated-fixture', 'preserve')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if version, err := sqliteSchemaVersion(ctx, db); err != nil || version != 71 {
		t.Fatalf("migration input is not schema 71: version=%d err=%v", version, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Exercise the production backup-first path, not goose.UpTo(72) directly.
	migrated, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if version, err := sqliteSchemaVersion(ctx, migrated.db); err != nil || version != centerSchemaVersion {
		t.Fatalf("migration did not reach current schema: version=%d err=%v", version, err)
	}
	for _, check := range []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM official_catalog_trust`, 0},
		{`SELECT COUNT(*) FROM pragma_table_info('deployments') WHERE name = 'pre_dispatch_application_status' AND dflt_value = '''failed''' AND "notnull" = 1`, 1},
		{`SELECT COUNT(*) FROM catalog_cache WHERE source_id='vastora-official'`, 0},
		{`SELECT COUNT(*) FROM settings WHERE key='official_catalog_signing_key'`, 0},
		{`SELECT COUNT(*) FROM catalog_cache WHERE source_id='private-fixture'`, 1},
		{`SELECT COUNT(*) FROM catalog_manifest_history WHERE manifest_sha256='retained-history'`, 1},
		{`SELECT COUNT(*) FROM settings WHERE key='unrelated-fixture' AND value='preserve'`, 1},
	} {
		var count int
		if err := migrated.db.QueryRowContext(ctx, check.query).Scan(&count); err != nil || count != check.want {
			t.Fatalf("%s: count=%d want=%d err=%v", check.query, count, check.want, err)
		}
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v71-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected one exact schema 71 pre-migration backup: %v err=%v", backups, err)
	}
	info, err := os.Stat(backups[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("migration backup must be owner-only: info=%v err=%v", info, err)
	}
	backupURL := url.URL{Scheme: "file", Path: backups[0], RawQuery: "mode=ro"}
	backup, err := sql.Open("sqlite", backupURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	backup.SetMaxOpenConns(1)
	if version, err := sqliteSchemaVersion(ctx, backup); err != nil || version != 71 {
		t.Fatalf("backup is not the pre-migration schema: version=%d err=%v", version, err)
	}
	for _, check := range []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='official_catalog_trust'`, 0},
		{`SELECT COUNT(*) FROM pragma_table_info('deployments') WHERE name = 'pre_dispatch_application_status'`, 0},
		{`SELECT COUNT(*) FROM settings WHERE key='official_catalog_signing_key' AND value='obsolete-local-key'`, 1},
		{`SELECT COUNT(*) FROM catalog_manifest_history WHERE source_id='vastora-official' AND app_id='pulse' AND version='0.1.0' AND manifest_sha256='retained-history'`, 1},
		{`SELECT COUNT(*) FROM settings WHERE key='unrelated-fixture' AND value='preserve'`, 1},
		{`SELECT COUNT(*) FROM goose_db_version WHERE version_id=71 AND is_applied=1`, 1},
		{`SELECT COUNT(*) FROM goose_db_version WHERE version_id=72 AND is_applied=1`, 0},
	} {
		var count int
		if err := backup.QueryRowContext(ctx, check.query).Scan(&count); err != nil || count != check.want {
			t.Fatalf("backup %s: count=%d want=%d err=%v", check.query, count, check.want, err)
		}
	}
	for _, fixture := range fixtures {
		// The backup retains both signed caches exactly, including the obsolete
		// local official cache that the live database is required to discard.
		var envelope []byte
		if err := backup.QueryRowContext(ctx, `SELECT envelope FROM catalog_cache WHERE source_id=?`, fixture.id).Scan(&envelope); err != nil || !bytes.Equal(envelope, fixture.envelope) {
			t.Fatalf("backup changed signed cache for %s: %v", fixture.id, err)
		}
		if fixture.id == "private-fixture" {
			if err := migrated.db.QueryRowContext(ctx, `SELECT envelope FROM catalog_cache WHERE source_id=?`, fixture.id).Scan(&envelope); err != nil || !bytes.Equal(envelope, fixture.envelope) {
				t.Fatalf("migration changed retained private cache: %v", err)
			}
		}
		for _, database := range []*sql.DB{backup, migrated.db} {
			var name, address, generation string
			var publicKey []byte
			var revision int
			if err := database.QueryRowContext(ctx, `SELECT display_name, url, generation, revision, public_key FROM catalog_sources WHERE id=?`, fixture.id).Scan(&name, &address, &generation, &revision, &publicKey); err != nil {
				t.Fatal(err)
			}
			if name != fixture.name || address != fixture.url || generation != fixture.generation || revision != 7 || !bytes.Equal(publicKey, fixture.publicKey) {
				t.Fatalf("migration or backup changed source identity for %s", fixture.id)
			}
		}
	}
}
