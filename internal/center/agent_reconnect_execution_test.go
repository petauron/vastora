package center

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
)

func TestAgentReconnectDoesNotApplyPreviousMachineResult(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	clock := store.now().UTC()
	store.now = func() time.Time { return clock }
	if _, err := store.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?),(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, agentConnectionModeSetting, "lan", agentConnectURLSetting, "https://center.example.com"); err != nil {
		t.Fatal(err)
	}
	node := enrollAccessTestNode(t, store, "reinstalled-node", "10.0.0.80")
	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"debug":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	const session = "previous-machine-execution-session"
	if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	task, err := store.claimExecutionTask(ctx, node.ID, node.Credential, session, 0)
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.StartExecution(ctx, node.ID, session, task.Authorization.ID, task.Authorization.Digest); err != nil {
		t.Fatal(err)
	}
	generation := task.RequiredRuntimeGeneration
	if err := store.StoreExecutionResult(ctx, node.ID, session, task.Authorization.ID, cpaApplicationResult("10.0.0.80"), true, false, "", &generation, false); err != nil {
		t.Fatal(err)
	}
	var retained []byte
	if err := store.db.QueryRow(`SELECT sealed_result FROM task_executions WHERE id=?`, task.Authorization.ID).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	enrollment, err := createReviewedReconnect(t, store, ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := store.EnrollAgent(ctx, enrollment.Token, "test", "linux", "amd64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	// A fresh machine may advertise the same supported runtime generation even
	// though none of the previous machine's applications or data exist on it.
	if _, err := store.db.Exec(`UPDATE agents SET runtime_generation=? WHERE id=?`, generation, node.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterExecutionSession(ctx, node.ID, replacement.Credential, "replacement-machine-execution-session", controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(taskLeaseDuration)
	if err := store.recoverExpiredReceivedExecutionResults(ctx); err != nil {
		t.Fatal(err)
	}
	var state, phase, disposition string
	var result []byte
	if err := store.db.QueryRow(`SELECT state,phase,disposition,sealed_result FROM task_executions WHERE id=?`, task.Authorization.ID).Scan(&state, &phase, &disposition, &result); err != nil {
		t.Fatal(err)
	}
	if state != "unknown" || phase != "result_received" || disposition != "" || !bytes.Equal(retained, result) {
		t.Fatalf("previous machine result was consumed: state=%s phase=%s disposition=%q evidence retained=%t", state, phase, disposition, bytes.Equal(retained, result))
	}
	var services int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM services WHERE application_id=?`, deployment.ApplicationID).Scan(&services); err != nil || services != 0 {
		t.Fatalf("old machine result published a service on the new machine: %d %v", services, err)
	}
	if err := store.RegisterExecutionSession(ctx, node.ID, replacement.Credential, session, controlplane.ExecutionProtocol); !errors.Is(err, errExecutionAuthorization) {
		t.Fatalf("old execution session revived: %v", err)
	}
	page, err := store.ListExecutions(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, execution := range page.Executions {
		if execution.ID == task.Authorization.ID && execution.CanConfirm {
			t.Fatal("old receipt offered as proof of completion on replacement machine")
		}
	}
	adminID := "reinstall-review-admin"
	if err := store.ConfirmExecution(ctx, task.Authorization.ID, adminID, controlplane.ExecutionDisposition{Action: "confirm-completed", ExecutionStopped: true, Note: "Previous machine was replaced"}); err == nil {
		t.Fatal("manual confirmation accepted the previous machine result")
	}
}

func TestAgentReconnectRetiresExecutionAuthorityAtomically(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "retire", true: "rollback"}[fail], func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			clock := store.now().UTC()
			store.now = func() time.Time { return clock }
			if _, err := store.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?),(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, agentConnectionModeSetting, "lan", agentConnectURLSetting, "https://center.example.com"); err != nil {
				t.Fatal(err)
			}
			node := enrollAccessTestNode(t, store, "reinstalled-node", "10.0.0.80")
			other := enrollAccessTestNode(t, store, "unchanged-node", "10.0.0.81")
			const session = "previous-machine-execution-session"
			for _, agent := range []AgentCredential{node, other} {
				if err := store.RegisterExecutionSession(ctx, agent.ID, agent.Credential, session, controlplane.ExecutionProtocol); err != nil {
					t.Fatal(err)
				}
			}
			for _, state := range []string{"offered", "running", "helper_running", "unknown", "failed", "succeeded"} {
				if _, err := store.db.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,sealed_result,state,phase,last_error,expires_at,created_at,updated_at)
					VALUES(?,?,?,'agent.update',1,?,'digest',X'01',X'02',?,'helper','original evidence',?,'','')`, state, node.ID, state, session, state, clock.Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
					t.Fatal(err)
				}
			}
			if fail {
				if _, err := store.db.Exec(`CREATE TRIGGER reject_retirement BEFORE UPDATE ON task_executions BEGIN SELECT RAISE(ABORT,'test refusal'); END`); err != nil {
					t.Fatal(err)
				}
			}
			clock = clock.Add(time.Minute)
			_, err := createReviewedReconnect(t, store, ctx, node.ID)
			if (err != nil) != fail {
				t.Fatalf("reconnect failure=%t: %v", fail, err)
			}
			if err := store.authenticateAgent(ctx, node.ID, node.Credential); (err == nil) != fail {
				t.Fatalf("credential revocation was not atomic: %v", err)
			}
			var active, otherActive int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_execution_sessions WHERE agent_id=?`, node.ID).Scan(&active); err != nil || active != map[bool]int{false: 0, true: 1}[fail] {
				t.Fatalf("session revocation was not atomic: %d %v", active, err)
			}
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_execution_sessions WHERE agent_id=?`, other.ID).Scan(&otherActive); err != nil || otherActive != 1 {
				t.Fatalf("unrelated session changed: %d %v", otherActive, err)
			}
			for _, original := range []string{"offered", "running", "helper_running", "unknown", "failed", "succeeded"} {
				var state, phase, note, retired, disposition string
				var task, result []byte
				if err := store.db.QueryRow(`SELECT state,phase,last_error,identity_retired_at,disposition,sealed_task,sealed_result FROM task_executions WHERE id=?`, original).Scan(&state, &phase, &note, &retired, &disposition, &task, &result); err != nil {
					t.Fatal(err)
				}
				want := original
				if !fail && (original == "offered" || original == "running" || original == "helper_running") {
					want = "unknown"
				}
				if state != want || phase != "helper" || note != "original evidence" || disposition != "" || (retired != "") != (!fail && original != "succeeded") || !bytes.Equal(task, []byte{1}) || !bytes.Equal(result, []byte{2}) {
					t.Fatalf("execution %s changed incorrectly: %s %s %q retired=%t", original, state, phase, note, retired != "")
				}
			}
			if !fail {
				for _, attempt := range []func() error{
					func() error { return store.StartExecution(ctx, node.ID, session, "offered", "digest") },
					func() error { return store.RenewExecution(ctx, node.ID, session, "running") },
					func() error { return store.CheckExecutionStep(ctx, node.ID, session, "running", "apply") },
					func() error { return store.CheckUpdateHelperStep(ctx, node.ID, session, "helper_running", "stop") },
					func() error {
						return store.StoreExecutionResult(ctx, node.ID, session, "helper_running", json.RawMessage(`{}`), true, false, "", nil, true)
					},
					func() error {
						return store.RegisterExecutionSession(ctx, node.ID, node.Credential, "late-original-machine-session", controlplane.ExecutionProtocol)
					},
				} {
					if err := attempt(); err == nil {
						t.Fatal("retired machine could advance an execution")
					}
				}
			}
		})
	}
}
