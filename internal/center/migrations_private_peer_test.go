package center

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVersion95PreservesAuthenticatedPrivatePeerObservation(t *testing.T) {
	directory := t.TempDir()
	previous := legacyMigrationStore(t, directory, 94)
	peer := `{"id":"agent-v3","publicKey":"nodekey:stable","address":"100.64.0.3"}`
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := previous.db.Exec(`INSERT INTO landing_client_capabilities(node_id,generation,peer_json,observed_at) VALUES('agent-v3',1,?,?)`, peer, stamp); err != nil {
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
	var observed, observedAt string
	var generation, version, violations int
	if err := upgraded.db.QueryRow(`SELECT generation,peer_json,observed_at FROM agent_private_peer_capabilities WHERE node_id='agent-v3'`).Scan(&generation, &observed, &observedAt); err != nil || generation != 1 || observed != peer || observedAt != stamp {
		t.Fatalf("private peer observation changed: generation=%d same_peer=%t same_time=%t err=%v", generation, observed == peer, observedAt == stamp, err)
	}
	if err := upgraded.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != centerSchemaVersion {
		t.Fatalf("schema version %d: %v", version, err)
	}
	if err := upgraded.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations %d: %v", violations, err)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v94-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatalf("pre-migration backup missing: %v %v", backups, err)
	}
	if info, err := os.Stat(backups[0]); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("pre-migration backup is not private: %v", err)
	}
	backup, err := sql.Open("sqlite", "file:"+backups[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err := backup.QueryRow(`SELECT generation,peer_json,observed_at FROM landing_client_capabilities WHERE node_id='agent-v3'`).Scan(&generation, &observed, &observedAt); err != nil || generation != 1 || observed != peer || observedAt != stamp {
		t.Fatalf("backup lost private peer observation: %v", err)
	}
}
