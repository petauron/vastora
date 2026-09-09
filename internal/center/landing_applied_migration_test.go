package center

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestLandingAppliedExitMigrationOnlyBackfillsConfirmedRevision(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		applied      int
		enabled      bool
		want         sql.NullString
	}{
		{"ready", "ready", 2, true, sql.NullString{String: "agent-v3", Valid: true}},
		{"restored", "stopped", 2, false, sql.NullString{Valid: true}},
		{"switching", "applying", 1, true, sql.NullString{}},
		{"failed_switch", "failed", 1, true, sql.NullString{}},
		{"first_enable", "pending", 0, true, sql.NullString{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			old := legacyMigrationStore(t, directory, 67)
			plan := `{"revision":2}`
			if tc.enabled {
				plan = `{"revision":2,"proxy":{}}`
			}
			_, err := old.db.Exec(`INSERT INTO landing_proxy_states(node_id,application_id,landing_node_id,server_revision,source_address,desired_revision,applied_revision,desired_json,status,updated_at)
 VALUES('agent-v3','application-v3','agent-v3',1,'100.64.0.1',2,?,?,?,'')`, tc.applied, plan, tc.status)
			if err != nil {
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
			var got sql.NullString
			if err := store.db.QueryRow(`SELECT applied_landing_node_id FROM landing_proxy_states`).Scan(&got); err != nil || got != tc.want {
				t.Fatalf("applied exit = %+v, want %+v: %v", got, tc.want, err)
			}
			backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v67-before-v%d-*.db", centerSchemaVersion)))
			if err != nil || len(backups) != 1 {
				t.Fatalf("missing migration backup: %v %v", backups, err)
			}
		})
	}
}
