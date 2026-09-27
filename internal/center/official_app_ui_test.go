package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func TestOfficialUIBundleCacheRejectsTamperingAndWrongVersion(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const name = "ui-meridian-1.2.3.js"
	bundle := []byte("export function mount() {}")
	hash := sha256.Sum256(bundle)
	target, err := metadata.TargetFile().FromBytes(name, bundle, "sha256")
	if err != nil {
		t.Fatal(err)
	}
	targets := metadata.Targets(time.Now().Add(time.Hour))
	targets.Signed.Targets[name] = target
	targetsRaw, err := json.Marshal(targets)
	if err != nil {
		t.Fatal(err)
	}
	trustRaw, err := json.Marshal(map[string][]byte{"targets": targetsRaw})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO official_catalog_trust(channel,revision,target_sha256,observed_at,expires_at,metadata_json,target) VALUES('stable',7,'hash','2026-01-01T00:00:00Z','2027-01-01T00:00:00Z',?,X'7b7d')`, trustRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO official_app_ui_assets(app_id,asset_kind,app_version,target_name,catalog_revision,sha256,bundle) VALUES('meridian','script','1.2.3',?,7,?,?)`, name, hex.EncodeToString(hash[:]), bundle); err != nil {
		t.Fatal(err)
	}
	got, err := store.OfficialUIAsset(context.Background(), "meridian", "1.2.3", "script")
	if err != nil || string(got) != string(bundle) {
		t.Fatalf("verified cached UI was unavailable: %v", err)
	}
	if _, err := store.OfficialUIAsset(context.Background(), "meridian", "1.2.4", "script"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wrong version returned %v", err)
	}
	if _, err := store.db.Exec(`UPDATE official_app_ui_assets SET bundle=X'00' WHERE app_id='meridian'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OfficialUIAsset(context.Background(), "meridian", "1.2.3", "script"); err == nil {
		t.Fatal("modified UI cache was served")
	}
	changed := []byte("export function mount() { alert('changed') }")
	changedHash := sha256.Sum256(changed)
	if _, err := store.db.Exec(`UPDATE official_app_ui_assets SET bundle=?, sha256=? WHERE app_id='meridian'`, changed, hex.EncodeToString(changedHash[:])); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OfficialUIAsset(context.Background(), "meridian", "1.2.3", "script"); err == nil {
		t.Fatal("cache content absent from signed target was served")
	}
}

func TestOfficialUIBundleEndpointRequiresSession(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/official-app-ui/meridian/1.2.3/bundle.js", nil)
	response := httptest.NewRecorder()
	NewServer(store, "", false).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated official UI request returned %d", response.Code)
	}
}

func TestOfficialUIBundleSchemaMigratesForwardWithBackup(t *testing.T) {
	directory := t.TempDir()
	previous := legacyMigrationStore(t, directory, 100)
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var version, count int
	if err := upgraded.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != centerSchemaVersion {
		t.Fatalf("schema version: %d %v", version, err)
	}
	if err := upgraded.db.QueryRow(`SELECT count(*) FROM official_app_ui_assets`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("UI cache was not created empty: %d %v", count, err)
	}
	if err := upgraded.db.QueryRow(`SELECT count(*) FROM official_app_ui_history`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("UI version history was not created empty: %d %v", count, err)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", "center-v100-before-v101-*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("pre-migration backup missing: %v %v", backups, err)
	}
}

func TestOfficialUIBundleVersionCannotBeReplaced(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	result := catalog.OfficialFetchResult{
		Catalog: catalog.Catalog{Apps: []catalog.AppManifest{{ID: "meridian", Version: "1.2.3"}}},
		UIBundles: map[string][]byte{
			"ui-meridian-1.2.3.js":  []byte("export const apiVersion = 1"),
			"ui-meridian-1.2.3.css": []byte("body { color: white }"),
		},
		State: catalog.OfficialFetchState{Acceptance: catalog.OfficialAcceptance{Revision: 7}},
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceOfficialUIBundles(ctx, tx, result); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	acceptedBundles := result.UIBundles
	result.UIBundles = nil
	tx, err = store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceOfficialUIBundles(ctx, tx, result); err == nil {
		t.Fatal("accepted Meridian UI was later omitted")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	result.UIBundles = acceptedBundles

	result.UIBundles["ui-meridian-1.2.3.js"] = []byte("export const apiVersion = 2")
	tx, err = store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceOfficialUIBundles(ctx, tx, result); err == nil {
		t.Fatal("same version accepted changed UI bytes")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM official_app_ui_assets WHERE app_id='meridian'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("rejected replacement changed accepted cache: %d %v", count, err)
	}
}
