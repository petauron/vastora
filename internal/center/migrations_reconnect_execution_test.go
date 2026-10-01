package center

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestVersion104RetainsExecutionsAndFailsClosed(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		directory := t.TempDir()
		previous := legacyMigrationStore(t, directory, 103)
		if _, err := previous.db.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,expires_at,created_at,updated_at)
			VALUES('retained','agent-v3','retained','application.apply',1,'session','digest',X'01','failed','prepare','','','')`); err != nil {
			t.Fatal(err)
		}
		if conflict {
			if _, err := previous.db.Exec(`ALTER TABLE task_executions ADD COLUMN identity_retired_at TEXT`); err != nil {
				t.Fatal(err)
			}
		}
		if err := previous.Close(); err != nil {
			t.Fatal(err)
		}
		upgraded, err := Open(directory)
		if conflict {
			if err == nil {
				upgraded.Close()
				t.Fatal("conflicting migration did not fail closed")
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			var state, retired string
			if err := upgraded.db.QueryRow(`SELECT state,identity_retired_at FROM task_executions WHERE id='retained'`).Scan(&state, &retired); err != nil || state != "failed" || retired != "" {
				t.Fatalf("migration invented replacement evidence: %s %q %v", state, retired, err)
			}
			if _, err := upgraded.db.Exec(`UPDATE task_executions SET identity_retired_at='fixture' WHERE id='retained'`); err != nil {
				t.Fatal(err)
			}
			var audit int
			if err := upgraded.db.QueryRow(`SELECT COUNT(*) FROM execution_events WHERE execution_id='retained'`).Scan(&audit); err != nil || audit != 2 {
				t.Fatalf("identity retirement was not audited: %d %v", audit, err)
			}
			upgraded.Close()
		}
		backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v103-before-v%d-*.db", centerSchemaVersion)))
		if err != nil || len(backups) != 1 {
			t.Fatalf("migration backup missing: %v", err)
		}
		db, err := sql.Open("sqlite", filepath.Join(directory, "center.db")+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		var version, violations int
		if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		if want := map[bool]int{false: centerSchemaVersion, true: 103}[conflict]; version != want {
			t.Fatalf("unexpected schema version: %d want %d", version, want)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
			t.Fatalf("foreign key failure: %d %v", violations, err)
		}
		db.Close()
	}
}
