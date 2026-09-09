package center

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestLocalRealityRemovalMigrationPreservesOperationsAndGuards(t *testing.T) {
	directory := t.TempDir()
	old := legacyMigrationStore(t, directory, 68)
	if _, err := old.db.Exec(`INSERT INTO application_commands(rowid,id,application_id,agent_id,gateway_node_id,kind,input_json,result_json,state,reconciliation_required,reconciliation_requested,attempt,lease_expires_at,error,created_at,updated_at)
		VALUES(71,'application-command-saved','application-v3','agent-v3','agent-v3','3xui.reality.rename','{"action":"rename","inboundId":9}','{"inboundId":9}','failed',1,1,4,'lease','saved failure','created','updated')`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`INSERT INTO secret_deliveries(kind,owner_id,operation_key_hash,request_hash,resource_id,state,created_at,updated_at) VALUES('application_command_result','owner',X'01',X'02','application-command-saved','pending','created','updated')`); err != nil {
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
	var rowID, attempt, required, requested int
	var input, result, state, lease, message, created, updated string
	if err := store.db.QueryRow(`SELECT rowid,attempt,reconciliation_required,reconciliation_requested,CAST(input_json AS TEXT),CAST(result_json AS TEXT),state,lease_expires_at,error,created_at,updated_at FROM application_commands WHERE id='application-command-saved'`).Scan(&rowID, &attempt, &required, &requested, &input, &result, &state, &lease, &message, &created, &updated); err != nil {
		t.Fatal(err)
	}
	if rowID != 71 || attempt != 4 || required != 1 || requested != 1 || input != `{"action":"rename","inboundId":9}` || result != `{"inboundId":9}` || state != "failed" || lease != "lease" || message != "saved failure" || created != "created" || updated != "updated" {
		t.Fatal("migration changed saved operation")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM secret_deliveries WHERE resource_id='application-command-saved'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration erased credential delivery: %d %v", count, err)
	}
	insert := `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) VALUES('application-command-remove','application-v3','agent-v3','agent-v3','3xui.reality.remove','{}','pending','','')`
	if _, err := store.db.Exec(insert); err == nil {
		t.Fatal("migration lost active operation exclusion")
	}
	if _, err := store.db.Exec(`UPDATE application_commands SET reconciliation_required=0 WHERE id='application-command-saved'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(insert); err != nil {
		t.Fatalf("new removal kind rejected: %v", err)
	}
	for _, name := range []string{"secret_deliveries_delete_with_application_command", "application_commands_one_active_idx", "application_commands_one_active_controller_idx", "application_commands_one_active_reality_name_idx", "application_commands_block_during_three_x_ui_migration", "application_command_updates_block_during_three_x_ui_migration", "deployments_block_during_three_x_ui_data_plane", "application_commands_block_during_three_x_ui_deployment", "application_command_updates_block_during_three_x_ui_deployment"} {
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("missing migration guard %s: %v", name, err)
		}
	}
	if _, err := store.db.Exec(`DELETE FROM application_commands WHERE id='application-command-saved'`); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM secret_deliveries WHERE resource_id='application-command-saved'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("delivery cleanup trigger missing: %d %v", count, err)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v68-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatalf("missing pre-migration backup: %v %v", backups, err)
	}
}
