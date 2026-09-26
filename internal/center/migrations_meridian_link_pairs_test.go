package center

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestVersion97PreservesLinkPairs(t *testing.T) {
	directory := t.TempDir()
	previous := legacyMigrationStore(t, directory, 96)
	if _, err := previous.db.Exec(`INSERT INTO node_diagnostic_checks(agent_id,kind,id,bind_address,target_revision,targets_json,state,error,created_at,updated_at)
 VALUES('agent-v3','meridian.link-bandwidth','original-link','',1,'{"sourceNodeId":"agent-v3","landingNodeId":"landing-one"}','succeeded','probe_failed','before','before'),
 ('agent-v3','meridian.link-bandwidth-server','original-server','',1,'{"sourceNodeId":"source-one","landingNodeId":"agent-v3"}','pending','','before','before'),
 ('agent-v3','node.host-profile','original-host','',1,'[]','succeeded','','before','before')`); err != nil {
		t.Fatal(err)
	}
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	for id, want := range map[string]string{"original-link": "landing-one", "original-server": "source-one", "original-host": ""} {
		var pair string
		if err := upgraded.db.QueryRow(`SELECT pair_key FROM node_diagnostic_checks WHERE id=?`, id).Scan(&pair); err != nil || pair != want {
			t.Fatalf("pair %s: got %q, want %q: %v", id, pair, want, err)
		}
	}
	if _, err := upgraded.db.Exec(`INSERT INTO node_diagnostic_checks(agent_id,kind,pair_key,id,bind_address,target_revision,targets_json,state,created_at,updated_at)
 VALUES('agent-v3','meridian.link-bandwidth','landing-two','second-link','',1,'{}','pending','after','after')`); err != nil {
		t.Fatalf("second pair rejected: %v", err)
	}
	var count, violations, version int
	if err := upgraded.db.QueryRow(`SELECT count(*) FROM node_diagnostic_checks WHERE kind='meridian.link-bandwidth'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("previous pair overwritten: %d %v", count, err)
	}
	if err := upgraded.db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations: %d %v", violations, err)
	}
	if err := upgraded.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != centerSchemaVersion {
		t.Fatalf("schema version: %d %v", version, err)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v96-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatalf("pre-migration backup missing: %v %v", backups, err)
	}
}
