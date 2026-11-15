package center

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func supersededHistoryFixture(t *testing.T) *Store {
	t.Helper()
	s := openMeridianSharedEndpointSnapshotFixture(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,created_at,updated_at) SELECT 'native','Native',node_id,site_id,'vastora-official/glivela-entry','','running','host',created_at,updated_at FROM applications WHERE id='snapshot-shared-app'`)
	exec(`INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,status,created_at,updated_at) SELECT 'native-service','native',site_id,'proxy',protocol,container_port,host_port,endpoint,source,'ready',created_at,updated_at FROM services WHERE id='snapshot-shared-service'`)
	exec(`INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,sni_hostname,dns_provider,tls_enabled,status,created_at,updated_at) SELECT 'native-publication','native-service',kind,ingress_owner,entry_node_id,'native.example.test',sni_hostname,dns_provider,tls_enabled,'ready',created_at,updated_at FROM publications WHERE id='snapshot-shared-publication'`)
	exec(`UPDATE publications SET status='stopped' WHERE id='snapshot-shared-publication'`)
	exec(`UPDATE meridian_endpoints SET legacy_retired=1 WHERE application_id='snapshot-shared-app'`)
	source := []byte(`{"applicationId":"snapshot-shared-app","appKey":"vastora-official/meridian","runtime":"docker","state":"ready","resources":[{"kind":"container","component":"xray","id":"old-container"}]}`)
	exec(`UPDATE application_resources SET resources_json=? WHERE application_id='snapshot-shared-app'`, source)
	native := []byte(`{"applicationId":"native","appKey":"vastora-official/glivela-entry","runtime":"native","state":"ready","resources":[{"kind":"unit","name":"glivela"}]}`)
	exec(`INSERT INTO application_resources(application_id,manifest_sha256,package_revision,authorized_capabilities_json,resources_json,adoption_state,updated_at) VALUES('native','',1,'[]',?,'ready','')`, native)
	evidence, _ := json.Marshal(GlivelaExecutionRetirement{ApplicationID: "snapshot-shared-app", ReplacementApplicationID: "native", SourceReceiptSHA256: rawManifestDigest(source), LegacyState: "execution-retired", LegacyStopped: true, RestartDisabled: true, NativeReady: true})
	exec(`INSERT INTO application_execution_retirements VALUES('snapshot-shared-app','native',?,'','')`, evidence)
	exec(`INSERT INTO deployments(id,agent_id,app_key,app_version,manifest_json,config_json,operation,state,created_at,updated_at,application_id) SELECT 'old-install',node_id,app_key,'test','{}','{}','install','succeeded','','',id FROM applications WHERE id='snapshot-shared-app'`)
	exec(`INSERT INTO application_adoptions(id,application_id,agent_id,deployment_id,state,created_at,updated_at) SELECT 'old-adoption',id,node_id,'old-install','succeeded','','' FROM applications WHERE id='snapshot-shared-app'`)
	exec(`INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) SELECT 'disposed-command',id,node_id,node_id,'meridian.runtime.apply','{}','failed','','' FROM applications WHERE id='snapshot-shared-app'`)
	exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,sealed_result,state,phase,expires_at,created_at,updated_at,disposition,disposition_note,disposition_actor,disposed_at) SELECT 'audit',node_id,'disposed-command','meridian.runtime.apply',1,'session','digest',X'0102',X'0304','unknown','result_received','','','','abandon','old decision','operator','original-time' FROM applications WHERE id='snapshot-shared-app'`)
	exec(`INSERT INTO task_events(id,task_id,agent_id,kind,event,created_at) SELECT 'audit-event','disposed-command',node_id,'test','failed','original-time' FROM applications WHERE id='snapshot-shared-app'`)
	exec(`INSERT INTO meridian_route_grants(id,account_id,endpoint_id,egress_node_id,base_credential_id,route_credential_id,enabled,status,created_at,updated_at) SELECT 'old-route',? ,?,node_id,?, ?,1,'ready','','' FROM applications WHERE id='snapshot-shared-app'`, sharedSnapshotAccountA, sharedSnapshotEndpointID, sharedSnapshotAccountA+"-native", sharedSnapshotAccountB+"-native")
	exec(`INSERT INTO meridian_route_health_events(route_id,recorded_at,fingerprint,diagnostics_json) VALUES('old-route','',?,'{}')`, strings.Repeat("a", 64))
	s.glivelaPilotStatePath = filepath.Join(t.TempDir(), "nodes.json")
	state, _ := json.Marshal([]any{map[string]any{"id": "snapshot-shared-app", "certificateId": "cert", "reportedAt": s.now(), "desired": map[string]any{"revision": 3, "configSha256": strings.Repeat("a", 64)}, "receipt": map[string]any{"revision": 3, "configSha256": strings.Repeat("a", 64), "runtimeReady": true}}})
	if err := os.WriteFile(s.glivelaPilotStatePath, state, 0600); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO retired_proxy_business VALUES(1,'{}')`)
	s.businessRetired = true
	return s
}

func TestSupersededHistoryPreservesDisposedAuditAndNativeState(t *testing.T) {
	s := supersededHistoryFixture(t)
	before := handoverTableHashes(t, s.db)
	// Preview must include all intended deletes, yet restore both rows and guards.
	r, err := s.PurgeSupersededGlivelaHistory(context.Background(), "snapshot-shared-app", "native", "shared-subscription-entry", false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Committed || r.PreservedExecutions != 1 || r.Deleted["publications"] != 1 || !reflect.DeepEqual(before, handoverTableHashes(t, s.db)) {
		t.Fatal("preview altered state or omitted expected scope", r)
	}
	r, err = s.PurgeSupersededGlivelaHistory(context.Background(), "snapshot-shared-app", "native", "shared-subscription-entry", true)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Committed || r.PreservedExecutions != 1 || r.Deleted["meridian_route_grants"] != 1 || r.Deleted["applications"] != 1 {
		t.Fatal(r)
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM applications WHERE id='snapshot-shared-app'`) != 0 || removalCount(t, s, `SELECT COUNT(*) FROM applications WHERE id='native' AND status='running'`) != 1 || removalCount(t, s, `SELECT COUNT(*) FROM publications WHERE id='native-publication' AND status='ready'`) != 1 {
		t.Fatal("wrong installation/publication deleted")
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM task_executions WHERE id='audit' AND disposition='abandon' AND disposition_actor='operator' AND disposed_at='original-time' AND sealed_task=X'0102' AND sealed_result=X'0304'`) != 1 || removalCount(t, s, `SELECT COUNT(*) FROM task_events WHERE id='audit-event'`) != 1 {
		t.Fatal("disposed audit modified")
	}
	if removalCount(t, s, `SELECT COUNT(*) FROM meridian_accounts`) != 2 {
		t.Fatal("shared accounts deleted")
	}
	if err := verifyCenterEncryptedState(context.Background(), s.db, s.key); err != nil {
		t.Fatal("backup ownership broken", err)
	}
	if _, err := s.db.Exec(`DELETE FROM meridian_accounts`); err == nil {
		t.Fatal("handover freeze lost")
	}
	if _, err := s.PurgeSupersededGlivelaHistory(context.Background(), "snapshot-shared-app", "native", "shared-subscription-entry", true); err == nil {
		t.Fatal("deleted source accepted again")
	}
}

func TestSupersededHistoryRejectsUnsafeDependenciesAndIdentity(t *testing.T) {
	for _, kind := range []string{"active-publication", "stale-identity", "shared-grant", "unresolved-execution", "late-failure"} {
		t.Run(kind, func(t *testing.T) {
			s := supersededHistoryFixture(t)
			var err error
			switch kind {
			case "active-publication":
				_, err = s.db.Exec(`UPDATE publications SET status='ready' WHERE id='snapshot-shared-publication'`)
			case "stale-identity":
				raw, _ := os.ReadFile(s.glivelaPilotStatePath)
				var rows []map[string]any
				_ = json.Unmarshal(raw, &rows)
				rows[0]["reportedAt"] = s.now().Add(-time.Minute)
				raw, _ = json.Marshal(rows)
				err = os.WriteFile(s.glivelaPilotStatePath, raw, 0600)
			case "shared-grant":
				_, err = s.db.Exec(`INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,status,created_at,updated_at) SELECT 'retained-service','native',site_id,'retained',protocol,container_port,host_port,endpoint,source,'ready','','' FROM services WHERE id='snapshot-shared-service'; CREATE TABLE retained_history(id TEXT PRIMARY KEY,credential_id TEXT REFERENCES meridian_credentials(id) ON DELETE CASCADE); INSERT INTO retained_history VALUES('keep','snapshot-account-a-native')`)
			case "unresolved-execution":
				_, err = s.db.Exec(`UPDATE task_executions SET disposition='' WHERE id='audit'`)
			case "late-failure":
				_, err = s.db.Exec(`CREATE TRIGGER reject_old_application_delete BEFORE DELETE ON applications WHEN OLD.id='snapshot-shared-app' BEGIN SELECT RAISE(ABORT,'injected failure'); END`)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := handoverTableHashes(t, s.db)
			if _, err := s.PurgeSupersededGlivelaHistory(context.Background(), "snapshot-shared-app", "native", "shared-subscription-entry", true); err == nil {
				t.Fatal("unsafe cleanup accepted")
			}
			if !reflect.DeepEqual(before, handoverTableHashes(t, s.db)) || removalCount(t, s, `SELECT COUNT(*) FROM applications WHERE id='snapshot-shared-app'`) != 1 {
				t.Fatal("failure did not roll back")
			}
			if _, err := s.db.Exec(`DELETE FROM meridian_accounts`); err == nil {
				t.Fatal("failure leaked freeze exception")
			}
		})
	}
}
