package center

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func addReinstallUnclaimedFixture(t *testing.T, store *Store, node AgentCredential) AgentReinstallLocalWorkInput {
	t.Helper()
	addReinstallApplication(t, store, node, "queued-app", "external/queued-app", "1.0.0", "install", "pending")
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO node_listener_states(node_id,desired_revision,applied_revision,desired_json,status,updated_at) VALUES(?,4,3,'{"listeners":[]}','pending',?)`, node.ID, stamp); err != nil {
		t.Fatal(err)
	}
	plan, err := store.AgentReinstallPlan(context.Background(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	return AgentReinstallLocalWorkInput{OperationID: plan.Recovery.ID, PlanRevision: plan.Revision, ConfirmLocal: true}
}

func TestAgentReinstallLocalSettlementCancelsOnlyNeverIssuedWork(t *testing.T) {
	store, node, _ := reinstallLocalWorkFixture(t)
	input := addReinstallUnclaimedFixture(t, store, node)
	ctx := context.Background()
	plan, err := store.AgentReinstallPlan(ctx, node.ID)
	if err != nil || len(plan.UnclaimedLocalWork) != 2 {
		t.Fatalf("unclaimed work missing: %+v %v", plan.UnclaimedLocalWork, err)
	}
	var beforeExecutions int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_executions`).Scan(&beforeExecutions); err != nil {
		t.Fatal(err)
	}
	result, err := store.SettleAgentReinstallLocalWork(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || !reflect.DeepEqual(result.UnclaimedWork, plan.UnclaimedLocalWork) {
		t.Fatalf("receipt did not bind cancelled work: %+v %v", result, err)
	}
	var state, config, listenerState, desired string
	var attempt, revision, applied int
	if err := store.db.QueryRow(`SELECT state,attempt,config_json FROM deployments WHERE id='queued-app-deployment'`).Scan(&state, &attempt, &config); err != nil || state != "failed" || attempt != 0 || config != `{"sensitive":"do-not-return-config"}` {
		t.Fatalf("unissued deployment changed intent or claimed success: %s %d %v", state, attempt, err)
	}
	if err := store.db.QueryRow(`SELECT status,attempt,desired_revision,applied_revision,desired_json FROM node_listener_states WHERE node_id=?`, node.ID).Scan(&listenerState, &attempt, &revision, &applied, &desired); err != nil || listenerState != "failed" || attempt != 0 || revision != 4 || applied != 3 || desired != `{"listeners":[]}` {
		t.Fatalf("listener revision/evidence rewritten: %s %d %d %v", listenerState, revision, applied, err)
	}
	var afterExecutions, cancelledEvents int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_executions`).Scan(&afterExecutions); err != nil || afterExecutions != beforeExecutions {
		t.Fatalf("cancellation manufactured execution evidence: %d %v", afterExecutions, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_events WHERE agent_id=? AND message LIKE 'Unissued task cancelled%'`, node.ID).Scan(&cancelledEvents); err != nil || cancelledEvents != 2 {
		t.Fatalf("missing cancellation events: %d %v", cancelledEvents, err)
	}
	after, err := store.AgentReinstallPlan(ctx, node.ID)
	if err != nil || len(after.UnclaimedLocalWork) != 0 || len(after.Executions) != 2 {
		t.Fatalf("uncertain external work altered or cancellation repeated: %+v %v", after, err)
	}
	again, err := store.SettleAgentReinstallLocalWork(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || !reflect.DeepEqual(again, result) {
		t.Fatalf("identical cancellation not idempotent: %+v %v", again, err)
	}
}

func TestAgentReinstallUnclaimedWorkRejectsPriorAuthorizationAndChangedIntent(t *testing.T) {
	for _, mode := range []string{"prior-authorization", "claimed-attempt", "changed-config", "transaction-failure"} {
		t.Run(mode, func(t *testing.T) {
			store, node, _ := reinstallLocalWorkFixture(t)
			input := addReinstallUnclaimedFixture(t, store, node)
			switch mode {
			case "prior-authorization":
				// Even a disposed historical execution makes attempt=0 insufficient.
				addReinstallSealedExecution(t, store, node.ID, "previous-listener", AgentTask{ID: nodeListenerTaskID(node.ID, 4), Kind: "node.listener.apply", Attempt: 1, Revision: 4})
				if _, err := store.db.Exec(`UPDATE task_executions SET disposition='abandon' WHERE id='previous-listener'`); err != nil {
					t.Fatal(err)
				}
			case "claimed-attempt":
				if _, err := store.db.Exec(`UPDATE node_listener_states SET attempt=1`); err != nil {
					t.Fatal(err)
				}
			case "changed-config":
				if _, err := store.db.Exec(`UPDATE deployments SET config_json='{"changed":true}' WHERE id='queued-app-deployment'`); err != nil {
					t.Fatal(err)
				}
			case "transaction-failure":
				if _, err := store.db.Exec(`CREATE TRIGGER fail_cancel BEFORE UPDATE ON node_listener_states BEGIN SELECT RAISE(ABORT,'fixture cancellation failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.SettleAgentReinstallLocalWork(context.Background(), node.ID, "reinstall-review-admin", input); err == nil {
				t.Fatal("stale or partially failed cancellation was accepted")
			}
			var cancelled int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM deployments WHERE id='queued-app-deployment' AND state='failed'`).Scan(&cancelled); err != nil || cancelled != 0 {
				t.Fatalf("cancellation leaked across rollback: %d %v", cancelled, err)
			}
			var localDispositions, receipts int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_executions WHERE id IN ('old-update','old-meridian') AND disposition<>''`).Scan(&localDispositions); err != nil || localDispositions != 0 {
				t.Fatalf("execution settlement leaked across rollback: %d %v", localDispositions, err)
			}
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_reinstall_local_dispositions`).Scan(&receipts); err != nil || receipts != 0 {
				t.Fatalf("partial receipt: %d %v", receipts, err)
			}
		})
	}
}

func TestAgentReinstallLocalSettlementAcceptsOnlyUnclaimedWork(t *testing.T) {
	store, node, initial := reinstallLocalWorkFixture(t)
	ctx := context.Background()
	if _, err := store.SettleAgentReinstallLocalWork(ctx, node.ID, "reinstall-review-admin", initial); err != nil {
		t.Fatal(err)
	}
	input := addReinstallUnclaimedFixture(t, store, node)
	result, err := store.SettleAgentReinstallLocalWork(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || len(result.ExecutionIDs) != 0 || len(result.UnclaimedWork) != 2 {
		t.Fatalf("unclaimed-only cancellation failed: %+v %v", result, err)
	}
	if blocked, err := agentReinstallBlocked(ctx, store.db, node.ID); err != nil || !blocked {
		t.Fatalf("cancellation activated replacement: %v", err)
	}
}

func TestAgentReinstallPlanBindsSuccessfulApplicationRestoreInput(t *testing.T) {
	store, node := reinstallPlanFixture(t)
	addReinstallApplication(t, store, node, "saved", meridianAppKey, "0.1.0-alpha.12", "install", "succeeded")
	ctx := context.Background()
	previous, err := store.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	queries := []string{
		`UPDATE deployments SET config_json='{"token":"secret-must-stay-hidden"}' WHERE id='saved-deployment'`,
		`UPDATE deployments SET manifest_json=json_set(manifest_json,'$.images[0].reference','ghcr.io/example/app@sha256:` + strings.Repeat("b", 64) + `') WHERE id='saved-deployment'`,
		`UPDATE deployments SET service_address='100.64.0.9' WHERE id='saved-deployment'`,
		`INSERT INTO secrets(id,sealed,created_at,updated_at) VALUES('saved-secret',X'1234','',''); UPDATE deployments SET secret_id='saved-secret' WHERE id='saved-deployment'`,
		`UPDATE secrets SET sealed=X'5678' WHERE id='saved-secret'`,
		`INSERT INTO registry_credentials(id,host,username,secret_id,created_at) VALUES('saved-registry','ghcr.io','user','saved-secret',''); UPDATE deployments SET registry_credential_id='saved-registry' WHERE id='saved-deployment'`,
		`UPDATE registry_credentials SET username='changed' WHERE id='saved-registry'`,
	}
	for _, query := range queries {
		if _, err := store.db.Exec(query); err != nil {
			t.Fatal(err)
		}
		current, err := store.AgentReinstallPlan(ctx, node.ID)
		if err != nil || current.Revision == previous.Revision {
			t.Fatalf("changed restoration input did not invalidate review: %v", err)
		}
		encoded, _ := json.Marshal(current)
		if strings.Contains(string(encoded), "secret-must-stay-hidden") || strings.Contains(string(encoded), "ghcr.io/example") || strings.Contains(string(encoded), "saved-secret") {
			t.Fatal("private saved input exposed in review")
		}
		previous = current
	}
	if _, err := store.db.Exec(`UPDATE deployments SET updated_at='2099-01-01T00:00:00Z' WHERE id='saved-deployment'`); err != nil {
		t.Fatal(err)
	}
	current, err := store.AgentReinstallPlan(ctx, node.ID)
	if err != nil || current.Revision != previous.Revision {
		t.Fatalf("irrelevant timestamp invalidated saved intent: %v", err)
	}
}
