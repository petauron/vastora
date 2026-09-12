package center

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/backupcrypto"
	"github.com/petauron/vastora/internal/catalog"
)

func TestOfficialCatalogRestoreRequiresRefreshAndPreservesReplayFloor(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	before, target, err := store.OfficialCatalogTrust(ctx, "stable")
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "center.backup")
	const password = "catalog-restore-test-password"
	if err := store.Backup(ctx, backup, password); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	if err := Restore(backup, destination, password); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	after, restoredTarget, err := restored.OfficialCatalogTrust(ctx, "stable")
	if err != nil {
		t.Fatal(err)
	}
	if after.Acceptance.Revision != before.Acceptance.Revision || after.Acceptance.SHA256 != before.Acceptance.SHA256 || string(target) != string(restoredTarget) || string(after.Metadata["root"]) != string(before.Metadata["root"]) {
		t.Fatal("restore discarded accepted identity or replay state")
	}
	if after.Acceptance.ExpiresAt.After(time.Now()) {
		t.Fatal("restored catalog still authorizes installation")
	}
	apps, err := restored.ListApps(ctx)
	if err != nil || len(apps) == 0 {
		t.Fatalf("restored cache unavailable for display: %v", err)
	}
	for _, app := range apps {
		if !app.InstallBlocked {
			t.Fatal("restored catalog permits installation")
		}
	}
	// Exercise the post-verification commit boundary using the original signed
	// target. Restore must not force publishers to invent a newer revision merely
	// to revalidate an otherwise current, unexpired publication.
	value, acceptance, err := catalog.ValidateOfficialTarget(target, "stable", after.Acceptance, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := restored.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result := catalog.OfficialFetchResult{Catalog: value, Target: target, State: catalog.OfficialFetchState{Acceptance: acceptance, Metadata: before.Metadata}}
	if err := commitOfficialCatalogTrust(ctx, tx, result, after.Acceptance, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	apps, err = restored.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range apps {
		if app.InstallBlocked {
			t.Fatal("verified refresh did not reauthorize catalog")
		}
	}
}

func TestBackupPasswordPolicyIsSharedByStoreRestoreAndWeb(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	output := filepath.Join(t.TempDir(), "short-password.vastora")
	if err := store.Backup(context.Background(), output, "too-short"); err == nil || !strings.Contains(err.Error(), "at least 12 characters") {
		t.Fatalf("Store.Backup short-password error = %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("short-password backup was created: %v", err)
	}
	if err := Restore(filepath.Join(t.TempDir(), "missing.vastora"), filepath.Join(t.TempDir(), "restore"), "too-short"); err == nil || !strings.Contains(err.Error(), "at least 12 characters") {
		t.Fatalf("Restore short-password error = %v", err)
	}

	server := NewServer(store, "", false)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/backups", strings.NewReader(`{"password":"too-short"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleCreateBackup(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) || strings.Contains(response.Body.String(), "at least 12 characters") {
		t.Fatalf("web short-password response = %d %q", response.Code, response.Body.String())
	}
}

func TestRestoreRejectsIncompatibleBackupVersionsBeforePublication(t *testing.T) {
	backupPath, password := createBackupForRestoreValidationTest(t)
	for _, test := range []struct {
		name   string
		mutate func(*backupMetadata, map[string][]byte)
	}{
		{name: "older-center", mutate: func(metadata *backupMetadata, _ map[string][]byte) { metadata.CenterVersion = "0.0.1" }},
		{name: "newer-center", mutate: func(metadata *backupMetadata, _ map[string][]byte) { metadata.CenterVersion = "999.0.0" }},
		{name: "older-schema", mutate: func(metadata *backupMetadata, _ map[string][]byte) { metadata.SchemaVersion-- }},
		{name: "newer-schema", mutate: func(metadata *backupMetadata, _ map[string][]byte) { metadata.SchemaVersion++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := rewriteBackupForRestoreValidationTest(t, backupPath, password, test.mutate)
			assertRestoreRejectedWithoutPublication(t, candidate, password)
		})
	}

	raw, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []byte{backupcrypto.Version - 1, backupcrypto.Version + 1} {
		candidate := filepath.Join(t.TempDir(), "format.vastora")
		modified := append([]byte(nil), raw...)
		modified[len(backupcrypto.Magic)] = version
		if err := os.WriteFile(candidate, modified, 0o600); err != nil {
			t.Fatal(err)
		}
		assertRestoreRejectedWithoutPublication(t, candidate, password)
	}
}

func TestRestoreValidatesSQLiteAndRootKeyBeforeAtomicPublication(t *testing.T) {
	backupPath, password := createBackupForRestoreValidationTest(t)
	for _, test := range []struct {
		name   string
		mutate func(*backupMetadata, map[string][]byte)
	}{
		{name: "damaged-database", mutate: func(_ *backupMetadata, files map[string][]byte) {
			files["center.db"][0] ^= 0xff
		}},
		{name: "wrong-root-key", mutate: func(_ *backupMetadata, files map[string][]byte) {
			files["center.key"] = make([]byte, len(files["center.key"]))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := rewriteBackupForRestoreValidationTest(t, backupPath, password, test.mutate)
			assertRestoreRejectedWithoutPublication(t, candidate, password)
		})
	}
}

func createBackupForRestoreValidationTest(t *testing.T) (string, string) {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	password := "restore-validation-password"
	path := filepath.Join(t.TempDir(), "center.vastora")
	if err := store.Backup(context.Background(), path, password); err != nil {
		t.Fatal(err)
	}
	return path, password
}

func rewriteBackupForRestoreValidationTest(t *testing.T, source, password string, mutate func(*backupMetadata, map[string][]byte)) string {
	t.Helper()
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := backupcrypto.Decrypt(raw, password)
	if err != nil {
		t.Fatal(err)
	}
	files, err := readArchive(plain)
	if err != nil {
		t.Fatal(err)
	}
	var metadata backupMetadata
	if err := json.Unmarshal(files["metadata.json"], &metadata); err != nil {
		t.Fatal(err)
	}
	delete(files, "metadata.json")
	mutate(&metadata, files)
	plain, err = archiveFiles(files, metadata)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = backupcrypto.Encrypt(plain, password)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "modified.vastora")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertRestoreRejectedWithoutPublication(t *testing.T, backupPath, password string) {
	t.Helper()
	parent := t.TempDir()
	destination := filepath.Join(parent, "restored")
	if err := Restore(backupPath, destination, password); err == nil {
		t.Fatal("invalid backup was restored")
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("failed restore published a destination: %v", err)
	}
	staging, err := filepath.Glob(filepath.Join(parent, ".restored.restore-*"))
	if err != nil || len(staging) != 0 {
		t.Fatalf("failed restore left staging directories: %v, err=%v", staging, err)
	}
}
