package center

import (
	"bytes"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestVersion105PreservesIntentAndFailsClosed(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		directory := t.TempDir()
		previous := legacyMigrationStore(t, directory, 104)
		if _, err := previous.db.Exec(`INSERT INTO agent_enrollment_tokens(token_hash,site_id,name,center_url,roles_json,capabilities_json,target_agent_id,expires_at)
   VALUES(X'1234','site-v3','Old reconnect','https://center.example.test','["worker"]','{}','agent-v3','2099-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		if _, err := previous.db.Exec(`INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,result_json,state,created_at,updated_at) VALUES('retained-command','application-v3','agent-v3','agent-v3','pulse.enrollment.create','{}','{"retained":true}','succeeded','','')`); err != nil {
			t.Fatal(err)
		}
		if _, err := previous.db.Exec(`INSERT INTO secret_deliveries(kind,owner_id,operation_key_hash,request_hash,resource_id,state,created_at,updated_at) VALUES('application_command_result','owner',X'01',X'02','retained-command','acknowledged','','')`); err != nil {
			t.Fatal(err)
		}
		var commandBefore []byte
		if err := previous.db.QueryRow(`SELECT json_array(rowid,id,application_id,agent_id,gateway_node_id,kind,input_json,result_json,state,created_at,updated_at) FROM application_commands WHERE id='retained-command'`).Scan(&commandBefore); err != nil {
			t.Fatal(err)
		}
		if conflict {
			if _, err := previous.db.Exec(`CREATE TABLE agent_reinstall_operations(id TEXT PRIMARY KEY)`); err != nil {
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
				t.Fatal("conflicting migration did not stop")
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			var operations, grants int
			if err = upgraded.db.QueryRow(`SELECT COUNT(*) FROM agent_reinstall_operations`).Scan(&operations); err != nil || operations != 0 {
				t.Fatalf("invented recovery: %d %v", operations, err)
			}
			if err = upgraded.db.QueryRow(`SELECT COUNT(*) FROM agent_enrollment_tokens WHERE target_agent_id IS NOT NULL`).Scan(&grants); err != nil || grants != 0 {
				t.Fatalf("unreviewed grant retained: %d %v", grants, err)
			}
			upgraded.Close()
		}
		backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v104-before-v%d-*.db", centerSchemaVersion)))
		if err != nil || len(backups) != 1 {
			t.Fatalf("backup missing: %v", err)
		}
		db, err := sql.Open("sqlite", filepath.Join(directory, "center.db")+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		var version, violations int
		if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		want := centerSchemaVersion
		if conflict {
			want = 104
		}
		if version != want {
			t.Fatalf("version %d want %d", version, want)
		}
		if err = db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
			t.Fatalf("FK failure: %d %v", violations, err)
		}
		var commandAfter []byte
		if err := db.QueryRow(`SELECT json_array(rowid,id,application_id,agent_id,gateway_node_id,kind,input_json,result_json,state,created_at,updated_at) FROM application_commands WHERE id='retained-command'`).Scan(&commandAfter); err != nil || !bytes.Equal(commandBefore, commandAfter) {
			t.Fatalf("historical command changed: %v", err)
		}
		var deliveries int
		if err := db.QueryRow(`SELECT COUNT(*) FROM secret_deliveries WHERE resource_id='retained-command'`).Scan(&deliveries); err != nil || deliveries != 1 {
			t.Fatalf("command delivery evidence lost: %v", err)
		}
		db.Close()
	}
}
