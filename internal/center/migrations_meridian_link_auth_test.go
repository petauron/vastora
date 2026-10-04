package center

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestVersion103StopsUnauthenticatedLinkTasks(t *testing.T) {
	directory := t.TempDir()
	previous := legacyMigrationStore(t, directory, 102)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	for _, state := range []string{"pending", "running", "succeeded"} {
		_, err := previous.db.Exec(`INSERT INTO node_diagnostic_checks(agent_id,kind,pair_key,id,bind_address,target_revision,targets_json,state,lease_expires_at,created_at,updated_at) VALUES('agent-v3','meridian.link-bandwidth',?,?,'',1,'{}',?,?,?,?)`, state, state, state, stamp, stamp, stamp)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := previous.db.Exec(`INSERT INTO node_diagnostic_checks(agent_id,kind,id,bind_address,target_revision,targets_json,state,created_at,updated_at) VALUES('agent-v3','node.host-profile','host','',1,'[]','succeeded',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := previous.db.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var count int
	if err := upgraded.db.QueryRow(`SELECT COUNT(*) FROM node_diagnostic_checks WHERE kind='meridian.link-bandwidth' AND state='failed' AND error='transport_unverified' AND lease_expires_at=''`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("old probes can resume: %d %v", count, err)
	}
	if err := upgraded.db.QueryRow(`SELECT COUNT(*) FROM node_diagnostic_checks WHERE id='host' AND state='succeeded'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("other diagnostics changed")
	}
	if err := upgraded.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&count); err != nil || count != 0 {
		t.Fatal("foreign key failure")
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v102-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatalf("migration backup missing: %v", err)
	}
}
