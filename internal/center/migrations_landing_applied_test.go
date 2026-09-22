package center

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

func TestVersion90PreservesOnlyConfirmedLandingAuthorization(t *testing.T) {
	directory := t.TempDir()
	legacy := legacyMigrationStore(t, directory, 89)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	want := map[string]string{}
	for _, test := range []struct {
		name, status string
		applied      uint64
		stop, trust  bool
	}{
		{name: "ready", status: "ready", applied: 3, trust: true},
		{name: "stopped", status: "stopped", applied: 3, stop: true, trust: true},
		{name: "pending", status: "pending", applied: 2},
		{name: "applying", status: "applying", applied: 2},
		{name: "failed", status: "failed", applied: 3},
		{name: "ready-mismatch", status: "ready", applied: 2},
		{name: "ready-without-plan", status: "ready", applied: 3, stop: true},
		{name: "stopped-with-plan", status: "stopped", applied: 3},
	} {
		nodeID := "v89-landing-" + test.name
		if _, err := legacy.db.Exec(`INSERT INTO agents(id,name,credential_hash,version,status,enrolled_at,last_seen_at,site_id)
			VALUES(?,?,?,'test','active',?,?,'site-v3')`, nodeID, nodeID, []byte(nodeID), stamp, stamp); err != nil {
			t.Fatal(err)
		}
		state := landing.ServerState{NodeID: nodeID, Revision: 3}
		if !test.stop {
			state.Plan = &landing.ServerPlan{Revision: 3, Address: "100.64.0.8", Sources: []landing.AuthorizedNode{{Address: "100.64.0.9", TCPOnly: true}}}
		}
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := legacy.db.Exec(`INSERT INTO landing_server_states(node_id,desired_revision,applied_revision,desired_json,peer_json,status,attempt,last_error,updated_at)
			VALUES(?,3,?,?,'{}',?,2,'preserve diagnostic',?)`, nodeID, test.applied, encoded, test.status, stamp); err != nil {
			t.Fatal(err)
		}
		want[nodeID] = "{}"
		if test.trust {
			want[nodeID] = string(encoded)
		}
	}
	before := landingVersion89PreservedRows(t, legacy.db)
	if err := legacy.db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migrated.Close() })
	if after := landingVersion89PreservedRows(t, migrated.db); after != before {
		t.Fatal("migration changed existing landing intent, identity, revision, or failure state")
	}
	for nodeID, expected := range want {
		var appliedJSON string
		if err := migrated.db.QueryRow(`SELECT applied_json FROM landing_server_states WHERE node_id=?`, nodeID).Scan(&appliedJSON); err != nil || appliedJSON != expected {
			t.Fatalf("applied authorization for %s: matches=%t err=%v", nodeID, appliedJSON == expected, err)
		}
	}
	if _, err := migrated.db.Exec(`UPDATE landing_server_states SET applied_json='not-json' WHERE node_id='v89-landing-ready'`); err == nil {
		t.Fatal("migrated schema accepted malformed applied state")
	}
	var version, violations int
	if err := migrated.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != centerSchemaVersion {
		t.Fatalf("migrated version=%d err=%v", version, err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations=%d err=%v", violations, err)
	}
	pattern := filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v89-before-v%d-*.db", centerSchemaVersion))
	backups, err := filepath.Glob(pattern)
	if err != nil || len(backups) != 1 {
		t.Fatalf("migration backup count=%d err=%v", len(backups), err)
	}
	info, err := os.Stat(backups[0])
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup permissions: %v", err)
	}
	backup, err := sql.Open("sqlite", "file:"+backups[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var columns int
	if err := backup.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 89 {
		t.Fatalf("backup version=%d err=%v", version, err)
	}
	if err := backup.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('landing_server_states') WHERE name='applied_json'`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("backup contains future applied state: columns=%d err=%v", columns, err)
	}
	if landingVersion89PreservedRows(t, backup) != before {
		t.Fatal("backup did not preserve the original landing state")
	}
	// A subsequent desired edit must not be promoted by reopening the database.
	if _, err := migrated.db.Exec(`UPDATE landing_server_states SET desired_revision=4,desired_json=json_set(desired_json,'$.revision',4,'$.plan.revision',4),status='pending'
		WHERE node_id='v89-landing-ready'`); err != nil {
		t.Fatal(err)
	}
	if err := migrated.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var reopenedJSON string
	if err := reopened.db.QueryRow(`SELECT applied_json FROM landing_server_states WHERE node_id='v89-landing-ready'`).Scan(&reopenedJSON); err != nil || reopenedJSON != want["v89-landing-ready"] {
		t.Fatalf("reopen promoted pending authorization: err=%v", err)
	}
	if repeated, err := filepath.Glob(pattern); err != nil || len(repeated) != 1 {
		t.Fatalf("reopen created extra backup: count=%d err=%v", len(repeated), err)
	}
}

func landingVersion89PreservedRows(t *testing.T, db *sql.DB) string {
	t.Helper()
	var value string
	if err := db.QueryRow(`SELECT json_group_array(json_object('node',node_id,'desiredRevision',desired_revision,'appliedRevision',applied_revision,
		'desired',CAST(desired_json AS TEXT),'peer',CAST(peer_json AS TEXT),'status',status,'attempt',attempt,'lease',lease_expires_at,'error',last_error,'updated',updated_at))
		FROM (SELECT * FROM landing_server_states ORDER BY node_id)`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
