package center

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestIPQualityEgressMigrationPreservesReportAndActiveLease(t *testing.T) {
	directory := t.TempDir()
	previous := legacyMigrationStore(t, directory, 98)
	report := `{"address":"203.0.113.8","version":"fixture","scores":[],"services":[]}`
	if _, err := previous.db.Exec(`INSERT INTO ip_quality_checks(agent_id,id,address,bind_address,state,attempt,lease_expires_at,error,result_json,checked_at,created_at,updated_at) VALUES('agent-v3','original-quality','203.0.113.8','10.0.0.18','running',4,'lease-before','timeout',?,'checked-before','created-before','updated-before')`, report); err != nil {
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
	var id, address, bind, state, lease, diagnostic, result, checked, created, updated string
	var attempt int
	if err := upgraded.db.QueryRow(`SELECT id,address,bind_address,state,attempt,lease_expires_at,error,result_json,checked_at,created_at,updated_at FROM ip_quality_checks WHERE agent_id='agent-v3'`).Scan(&id, &address, &bind, &state, &attempt, &lease, &diagnostic, &result, &checked, &created, &updated); err != nil {
		t.Fatal(err)
	}
	if id != "original-quality" || address != "203.0.113.8" || bind != "10.0.0.18" || state != "running" || attempt != 4 || lease != "lease-before" || diagnostic != "timeout" || result != report || checked != "checked-before" || created != "created-before" || updated != "updated-before" {
		t.Fatal("migration changed retained report or active task metadata")
	}
	if _, err := upgraded.db.Exec(`INSERT INTO ip_quality_checks(agent_id,id,address,bind_address,state,created_at,updated_at) VALUES('agent-v3','second-quality','2001:4860:4860::8888','2001:4860:4860::8888','pending','after','after')`); err == nil {
		t.Fatal("two active diagnostics for one agent were accepted")
	}
	if _, err := upgraded.db.Exec(`UPDATE ip_quality_checks SET state='succeeded' WHERE id='original-quality'`); err != nil {
		t.Fatal(err)
	}
	if _, err := upgraded.db.Exec(`INSERT INTO ip_quality_checks(agent_id,id,address,bind_address,state,created_at,updated_at) VALUES('agent-v3','second-quality','2001:4860:4860::8888','2001:4860:4860::8888','pending','after','after')`); err != nil {
		t.Fatalf("second egress rejected: %v", err)
	}
	var count, violations, version int
	if err := upgraded.db.QueryRow(`SELECT count(*) FROM ip_quality_checks WHERE agent_id='agent-v3'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("old egress overwritten: %d %v", count, err)
	}
	if err := upgraded.db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations: %d %v", violations, err)
	}
	if err := upgraded.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != centerSchemaVersion {
		t.Fatalf("schema version: %d %v", version, err)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v98-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatalf("pre-migration backup missing: %v %v", backups, err)
	}
}
