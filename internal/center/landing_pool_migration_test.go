package center

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobalLandingPoolMigrationRemovesPerEntryPolicy(t *testing.T) {
	directory := t.TempDir()
	old := legacyMigrationStore(t, directory, 78)
	if _, err := old.db.Exec(`INSERT INTO settings(key,value) VALUES
		(?, '{"nodeIds":["landing"],"revision":4}'),
		('node-exits:entry', '{"applicationId":"entry","ownExit":true,"landingNodeIds":["landing"],"landingRegionCodes":{"landing":"US"},"revision":2}'),
		('node-exits-error:entry', 'failed')`, landingSelectionKey); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var obsolete int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key LIKE 'node-exits:%'`).Scan(&obsolete); err != nil || obsolete != 0 {
		t.Fatalf("obsolete policies remain: %d %v", obsolete, err)
	}
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := readLandingSelection(context.Background(), tx)
	tx.Rollback()
	if err != nil || selection.LandingRegionCodes["landing"] != "US" || selection.Revision != 4 {
		t.Fatalf("global selection not preserved: %+v %v", selection, err)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", "center-v78-before-v81-*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("migration backup missing: %v %v", backups, err)
	}
}

func TestGlobalLandingPoolMigrationRefusesUnresolvedWork(t *testing.T) {
	directory := t.TempDir()
	old := legacyMigrationStore(t, directory, 78)
	if _, err := old.db.Exec(`INSERT INTO secrets(id,sealed,created_at,updated_at) VALUES('landing-secret',X'01','now','now');
		INSERT INTO three_x_ui_client_accounts(id,controller_id,email,metadata_json,observed_at)
		VALUES('parent','application-v3','Client','{"id":"parent","email":"Client","enabled":true}','now');
		INSERT INTO landing_client_grants(id,parent_id,application_id,service_id,landing_node_id,source_peer_json,grant_json,credential_secret_id,desired_revision,status,updated_at)
		VALUES('pending-grant','parent','application-v3','service-v3','agent-v3','{}','{"mode":"fixed"}','landing-secret',1,'preparing','now')`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(directory); err == nil {
		store.Close()
		t.Fatal("migration accepted unresolved landing work")
	} else if !strings.Contains(err.Error(), "resolve_landing_operations_before_upgrade") {
		t.Fatalf("unexpected migration failure: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 78 {
		t.Fatalf("failed migration advanced schema: %d %v", version, err)
	}
}
