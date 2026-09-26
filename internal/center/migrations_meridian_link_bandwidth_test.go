package center

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVersion96PreservesDiagnosticsAndAllowsMeridianLink(t *testing.T) {
	directory := t.TempDir()
	previous := legacyMigrationStore(t, directory, 95)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := previous.db.Exec(`INSERT INTO node_diagnostic_checks(agent_id,kind,id,bind_address,target_revision,targets_json,state,created_at,updated_at) VALUES('agent-v3','node.host-profile','old-host','',1,'[]','succeeded',?,?)`, stamp, stamp); err != nil {
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
	var kind string
	if err := upgraded.db.QueryRow(`SELECT kind FROM node_diagnostic_checks WHERE id='old-host'`).Scan(&kind); err != nil || kind != "node.host-profile" {
		t.Fatalf("existing diagnostic lost: %q %v", kind, err)
	}
	if _, err := upgraded.db.Exec(`INSERT INTO node_diagnostic_checks(agent_id,kind,id,bind_address,target_revision,targets_json,state,created_at,updated_at) VALUES('agent-v3','meridian.link-bandwidth','new-link','',1,'{"sourceNodeId":"agent-v3","landingNodeId":"landing","sourceIp":"100.64.0.3","landingIp":"100.64.0.4","port":30000}','pending',?,?)`, stamp, stamp); err != nil {
		t.Fatalf("new diagnostic kind rejected: %v", err)
	}
	var version, violations int
	if err := upgraded.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != centerSchemaVersion {
		t.Fatalf("schema version %d: %v", version, err)
	}
	if err := upgraded.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations %d: %v", violations, err)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v95-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatalf("pre-migration backup missing: %v %v", backups, err)
	}
	if info, err := os.Stat(backups[0]); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup not private: %v", err)
	}
}
