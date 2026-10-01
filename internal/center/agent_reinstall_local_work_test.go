package center

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/secret"
)

func addReinstallSealedExecution(t *testing.T, store *Store, agentID, id string, task AgentTask) {
	t.Helper()
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := secret.Seal(store.key, raw, []byte("execution-task:"+id))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	if _, err = store.db.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,sealed_result,state,phase,expires_at,created_at,updated_at)
	 VALUES(?,?,?,?,?,'previous-machine-session',?,? ,?,'unknown','result_received',?,?,?)`, id, agentID, task.ID, task.Kind, task.Attempt, hex.EncodeToString(digest[:]), sealed, []byte("encrypted-retained-result"), stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func reinstallLocalWorkFixture(t *testing.T) (*Store, AgentCredential, AgentReinstallLocalWorkInput) {
	t.Helper()
	store, node := prepareReinstallNode(t)
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO agent_updates(id,agent_id,target_version,state,attempt,created_at,updated_at) VALUES('old-update',?,?,'installing',1,?,?)`, node.ID, Version, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	addReinstallSealedExecution(t, store, node.ID, "old-update", AgentTask{ID: "old-update", Kind: "agent.update", Attempt: 1, TargetVersion: Version})
	for _, app := range []struct{ id, key string }{{"meridian", meridianAppKey}, {"collector", pulseAgentAppKey}} {
		addReinstallApplication(t, store, node, app.id, app.key, "0.1.0-alpha.12", "install", "running")
		if _, err := store.db.Exec(`UPDATE deployments SET attempt=1 WHERE id=?`, app.id+"-deployment"); err != nil {
			t.Fatal(err)
		}
		var manifest []byte
		if err := store.db.QueryRow(`SELECT manifest_json FROM deployments WHERE id=?`, app.id+"-deployment").Scan(&manifest); err != nil {
			t.Fatal(err)
		}
		task := AgentTask{ID: app.id + "-deployment", Kind: "application.apply", Attempt: 1, AppKey: app.key, ApplicationID: app.id, Operation: "install"}
		if err := json.Unmarshal(manifest, &task.Manifest); err != nil {
			t.Fatal(err)
		}
		addReinstallSealedExecution(t, store, node.ID, "old-"+app.id, task)
	}
	monitor := addReinstallRemoteCommand(t, store, node)
	if _, err := store.db.Exec(`UPDATE application_commands SET state='running',attempt=1 WHERE id='remote-enrollment'`); err != nil {
		t.Fatal(err)
	}
	addReinstallSealedExecution(t, store, monitor.ID, "old-remote", AgentTask{ID: "remote-enrollment", Kind: "application.command", Attempt: 1})
	enrollment, err := createReviewedReconnect(t, store, context.Background(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := store.EnrollAgent(context.Background(), enrollment.Token, Version, "linux", "amd64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := store.AgentReinstallPlan(context.Background(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	return store, replacement, AgentReinstallLocalWorkInput{OperationID: plan.Recovery.ID, PlanRevision: plan.Revision, ConfirmLocal: true}
}

func TestAgentReinstallLocalSettlementPreservesHistoryAndRemoteWork(t *testing.T) {
	store, node, input := reinstallLocalWorkFixture(t)
	ctx := context.Background()
	plan, err := store.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, execution := range plan.Executions {
		want := "manual_review"
		if execution.ID == "old-meridian" || execution.ID == "old-update" {
			want = "local_after_isolation"
		}
		if execution.Resolution != want {
			t.Fatalf("incorrect classification: %+v", execution)
		}
	}
	result, err := store.SettleAgentReinstallLocalWork(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || !reflect.DeepEqual(result.ExecutionIDs, []string{"old-update", "old-meridian"}) {
		t.Fatalf("wrong local settlement: %+v %v", result, err)
	}
	for _, id := range []string{"old-meridian", "old-update", "old-collector", "old-remote"} {
		var state, phase, disposition string
		var evidence []byte
		if err := store.db.QueryRow(`SELECT state,phase,disposition,sealed_result FROM task_executions WHERE id=?`, id).Scan(&state, &phase, &disposition, &evidence); err != nil {
			t.Fatal(err)
		}
		want := ""
		if slices.Contains(result.ExecutionIDs, id) {
			want = "abandon"
		}
		if state != "unknown" || phase != "result_received" || disposition != want || string(evidence) != "encrypted-retained-result" {
			t.Fatalf("outcome/evidence rewritten: %s %s %s %s", id, state, phase, disposition)
		}
	}
	var version, config, state string
	if err := store.db.QueryRow(`SELECT app_version,config_json,state FROM deployments WHERE id='meridian-deployment'`).Scan(&version, &config, &state); err != nil || version != "0.1.0-alpha.12" || config != `{"sensitive":"do-not-return-config"}` || state != "failed" {
		t.Fatalf("saved intent was changed: version=%q state=%q err=%v", version, state, err)
	}
	if blocked, err := agentReinstallBlocked(ctx, store.db, node.ID); err != nil || !blocked {
		t.Fatalf("settlement released recovery fence: %v", err)
	}
	after, err := store.AgentReinstallPlan(ctx, node.ID)
	if err != nil || len(after.Executions) != 2 || after.LocalWorkDisposition == nil || !reflect.DeepEqual(*after.LocalWorkDisposition, result) {
		t.Fatalf("receipt or manual review missing: %+v %v", after.LocalWorkDisposition, err)
	}
	// Recover a lost settlement response after a Center restart without writing
	// another disposition, touching the remaining commands or starting work.
	var seq int
	var name, databasePath string
	if err := store.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &databasePath); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(filepath.Dir(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, err := reopened.SettleAgentReinstallLocalWork(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || !reflect.DeepEqual(again, result) {
		t.Fatalf("lost response not recovered: %+v %v", again, err)
	}
	var receipts int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM agent_reinstall_local_dispositions`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("duplicate receipt: %d %v", receipts, err)
	}
}

func TestAgentReinstallLocalSettlementFailsClosed(t *testing.T) {
	for _, mode := range []string{"unconfirmed", "wrong-admin", "wrong-operation", "stale-review", "isolation-pending", "not-enrolled", "identity-changed", "credential-revoked", "evidence-changed", "attempt-changed", "execution-write-failure", "receipt-write-failure"} {
		t.Run(mode, func(t *testing.T) {
			store, node, input := reinstallLocalWorkFixture(t)
			adminID := "reinstall-review-admin"
			switch mode {
			case "unconfirmed":
				input.ConfirmLocal = false
			case "wrong-admin":
				adminID = "another-admin"
			case "wrong-operation":
				input.OperationID = "another-operation"
			default:
				query := map[string]string{
					"stale-review":            `UPDATE deployments SET config_json='{"changed":true}' WHERE id='meridian-deployment'`,
					"isolation-pending":       `UPDATE agent_reinstall_operations SET private_isolation='pending'`,
					"not-enrolled":            `UPDATE agent_reinstall_operations SET state='awaiting_enrollment',replacement_fingerprint=''`,
					"identity-changed":        `UPDATE agents SET x25519_public_key=X'1234'`,
					"credential-revoked":      `UPDATE agents SET credential_revoked_at='2026-01-01T00:00:00Z'`,
					"evidence-changed":        `UPDATE task_executions SET sealed_task=X'0000' WHERE id='old-meridian'`,
					"attempt-changed":         `UPDATE deployments SET attempt=attempt+1 WHERE id='meridian-deployment'`,
					"execution-write-failure": `CREATE TRIGGER fail_local_settlement BEFORE UPDATE ON task_executions WHEN NEW.id='old-meridian' AND NEW.disposition='abandon' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`,
					"receipt-write-failure":   `CREATE TRIGGER fail_settlement_receipt BEFORE INSERT ON agent_reinstall_local_dispositions BEGIN SELECT RAISE(ABORT,'receipt unavailable'); END`,
				}[mode]
				if _, err := store.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.SettleAgentReinstallLocalWork(context.Background(), node.ID, adminID, input); err == nil {
				t.Fatal("invalid settlement accepted")
			}
			var dispositions, receipts int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_executions WHERE disposition<>''`).Scan(&dispositions); err != nil || dispositions != 0 {
				t.Fatalf("partial disposition survived: %d %v", dispositions, err)
			}
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_reinstall_local_dispositions`).Scan(&receipts); err != nil || receipts != 0 {
				t.Fatalf("partial receipt survived: %d %v", receipts, err)
			}
			var state string
			if err := store.db.QueryRow(`SELECT state FROM deployments WHERE id='meridian-deployment'`).Scan(&state); err != nil || state != "running" {
				t.Fatalf("business attempt was partially abandoned: %q %v", state, err)
			}
		})
	}
}

func TestAgentReinstallLocalSettlementDoesNotOverwriteNewerIntent(t *testing.T) {
	store, node, _ := reinstallLocalWorkFixture(t)
	if _, err := store.db.Exec(`UPDATE deployments SET state='failed' WHERE id='meridian-deployment'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO deployments(id,agent_id,app_key,app_version,manifest_json,config_json,operation,state,created_at,updated_at,application_id)
	 SELECT 'new-uninstall',agent_id,app_key,app_version,manifest_json,config_json,'uninstall','pending','2099-01-01T00:00:00Z','2099-01-01T00:00:00Z',application_id FROM deployments WHERE id='meridian-deployment'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE applications SET status='pending' WHERE id='meridian'`); err != nil {
		t.Fatal(err)
	}
	plan, err := store.AgentReinstallPlan(context.Background(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SettleAgentReinstallLocalWork(context.Background(), node.ID, "reinstall-review-admin", AgentReinstallLocalWorkInput{OperationID: plan.Recovery.ID, PlanRevision: plan.Revision, ConfirmLocal: true}); err != nil {
		t.Fatal(err)
	}
	var appState, taskState string
	if err := store.db.QueryRow(`SELECT a.status,d.state FROM applications a JOIN deployments d ON d.application_id=a.id WHERE d.id='new-uninstall'`).Scan(&appState, &taskState); err != nil || appState != "pending" || taskState != "pending" {
		t.Fatalf("settlement changed newer uninstall intent: %s %s %v", appState, taskState, err)
	}
}

func TestAgentReinstallLocalSettlementAPIRequiresAdminAndCSRF(t *testing.T) {
	store, node, input := reinstallLocalWorkFixture(t)
	if _, err := store.db.Exec(`DELETE FROM admins`); err != nil {
		t.Fatal(err)
	}
	session, csrf, err := store.CreateFirstAdmin(context.Background(), "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	adminID, err := store.SessionAdminID(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agent_reinstall_operations SET authorized_by=?`, adminID); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(input)
	handler := NewServer(store, "", false).Handler()
	for _, test := range []struct {
		auth, csrf bool
		status     int
	}{{false, false, 401}, {true, false, 401}, {true, true, 200}, {true, true, 200}} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+node.ID+"/reinstall-work/settle", bytes.NewReader(encoded))
		request.Header.Set("Content-Type", "application/json")
		if test.auth {
			request.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
		}
		if test.csrf {
			request.AddCookie(&http.Cookie{Name: "vastora_csrf", Value: csrf})
			request.Header.Set("X-CSRF-Token", csrf)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
		}
		if response.Code == 200 && response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("settlement response can be cached")
		}
	}
}
