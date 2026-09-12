package center

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestOfflineNodeRemovalMigrationPreservesExistingInstallations(t *testing.T) {
	directory := t.TempDir()
	old := legacyMigrationStore(t, directory, 73)
	var apps, nodes int
	if err := old.db.QueryRow(`SELECT COUNT(*) FROM applications`).Scan(&apps); err != nil {
		t.Fatal(err)
	}
	if err := old.db.QueryRow(`SELECT COUNT(*) FROM agents`).Scan(&nodes); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if removalCount(t, store, `SELECT COUNT(*) FROM applications`) != apps || removalCount(t, store, `SELECT COUNT(*) FROM agents`) != nodes {
		t.Fatal("migration removed existing data")
	}
	if removalCount(t, store, `SELECT COUNT(*) FROM agent_removals`) != 0 {
		t.Fatal("migration started an unrequested removal")
	}
	if _, err = store.db.Exec(`INSERT INTO agent_removals(agent_id,state,prepared,headscale_done,headscale_identity_json,created_at,updated_at) VALUES('agent-v3','failed',1,1,'{"id":"10"}','','')`); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if removalCount(t, store, `SELECT COUNT(*) FROM agent_removals WHERE agent_id='agent-v3' AND state='failed' AND prepared=1 AND headscale_done=1 AND json_extract(headscale_identity_json,'$.id')='10'`) != 1 {
		t.Fatal("restart lost cleanup progress")
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v73-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatalf("missing pre-migration backup: %v %v", backups, err)
	}
}
