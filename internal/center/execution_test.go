package center

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/secret"
)

func TestExecutionAuthorizationCannotReplayAfterLostResponseOrRestart(t *testing.T) {
	for _, failure := range []string{"offer lost", "start response lost", "process restarted", "result response lost"} {
		t.Run(failure, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			clock := store.now().UTC()
			store.now = func() time.Time { return clock }
			node := enrollOrchestrationNode(t, store, "execution-test", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.16", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.16", LANAddress: "10.0.0.16", EnabledKinds: []string{networking.KindLAN}})
			session := "execution-session-for-current-process"
			if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
				t.Fatal(err)
			}
			heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.123", true)
			update, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.124")
			if err != nil {
				t.Fatal(err)
			}
			task := AgentTask{ID: "execution-task", Kind: "application.apply", Attempt: 1, Secrets: json.RawMessage(`{"token":"private-test-value"}`)}
			auth, err := store.PersistExecutionAuthorization(ctx, node.ID, session, task)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.executionClaimAllowed(ctx, node.ID, session); !errors.Is(err, errExecutionBlocked) {
				t.Fatalf("offered task did not fence another execution: %v", err)
			}
			if _, err := store.ClaimNextTask(ctx, node.ID, node.Credential); !errors.Is(err, errExecutionBlocked) {
				t.Fatalf("Store claim bypassed persisted authorization fence: %v", err)
			}
			var updateState string
			var updateAttempt int64
			if err := store.db.QueryRowContext(ctx, `SELECT state,attempt FROM agent_updates WHERE id=?`, update.ID).Scan(&updateState, &updateAttempt); err != nil || updateState != "pending" || updateAttempt != 0 {
				t.Fatalf("self-update bypassed execution fence: state=%s attempt=%d err=%v", updateState, updateAttempt, err)
			}
			if failure != "offer lost" {
				if err := store.StartExecution(ctx, node.ID, session, auth.ID, auth.Digest); err != nil {
					t.Fatal(err)
				}
				if err := store.StartExecution(ctx, node.ID, session, auth.ID, auth.Digest); !errors.Is(err, errExecutionAuthorization) {
					t.Fatalf("start offer was consumed twice: %v", err)
				}
			}
			if failure == "result response lost" {
				if err := store.StoreExecutionResult(ctx, node.ID, session, auth.ID, json.RawMessage(`{"secret":"result-secret"}`), true, false, "", nil); err != nil {
					t.Fatal(err)
				}
				if err := store.executionClaimAllowed(ctx, node.ID, session); !errors.Is(err, errExecutionBlocked) {
					t.Fatalf("result released fence before business commit: %v", err)
				}
			}
			if failure == "process restarted" {
				if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, "execution-session-for-replacement-process", controlplane.ExecutionProtocol); err != nil {
					t.Fatal(err)
				}
				if err := store.CheckExecutionStep(ctx, node.ID, session, auth.ID, "apply"); !errors.Is(err, errExecutionAuthorization) {
					t.Fatalf("old process retained execution permission: %v", err)
				}
				session = "execution-session-for-replacement-process"
			}
			clock = clock.Add(taskLeaseDuration + time.Second)
			if err := store.executionClaimAllowed(ctx, node.ID, session); err != nil {
				t.Fatalf("terminal business evidence blocked the independent Agent update: %v", err)
			}
			views, err := executionViewsForTest(ctx, store)
			if err != nil || len(views) != 1 || views[0].State != "unknown" || views[0].Attempt != 1 {
				t.Fatalf("missing unknown execution evidence: %#v %v", views, err)
			}
			encoded, _ := json.Marshal(views)
			if bytes.Contains(encoded, []byte("private-test-value")) || bytes.Contains(encoded, []byte("result-secret")) {
				t.Fatal("execution list exposed secrets")
			}
			var sealed []byte
			if err := store.db.QueryRowContext(ctx, `SELECT sealed_task FROM task_executions WHERE id=?`, auth.ID).Scan(&sealed); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(sealed, []byte("private-test-value")) {
				t.Fatal("task secrets persisted in plaintext")
			}
			if raw, err := secret.Open(store.key, sealed, []byte("execution-task:"+auth.ID)); err != nil || !bytes.Contains(raw, []byte("private-test-value")) {
				t.Fatalf("encrypted evidence cannot be recovered: %v", err)
			}
		})
	}
}

func TestExecutionRejectsProtocolMismatchAndDatabaseFailure(t *testing.T) {
	store := openOrchestrationStore(t)
	ctx := context.Background()
	if err := store.RegisterExecutionSession(ctx, "unknown", "credential", "execution-session-for-current-process", controlplane.ExecutionProtocol-1); !errors.Is(err, errExecutionAuthorization) {
		t.Fatalf("accepted obsolete protocol: %v", err)
	}
	store.Close()
	if _, err := store.PersistExecutionAuthorization(ctx, "unknown", "session", AgentTask{ID: "task", Attempt: 1}); err == nil {
		t.Fatal("issued an authorization while storage was unavailable")
	}
}

func TestExecutionFailureRemainsFencedAndSuccessRequiresResultCommit(t *testing.T) {
	for _, succeeded := range []bool{false, true} {
		store := openOrchestrationStore(t)
		ctx := context.Background()
		node := enrollOrchestrationNode(t, store, "execution-result", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.17", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.17", LANAddress: "10.0.0.17", EnabledKinds: []string{networking.KindLAN}})
		session := "execution-result-session-for-current-process"
		if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
			t.Fatal(err)
		}
		auth, err := store.PersistExecutionAuthorization(ctx, node.ID, session, AgentTask{ID: "result-task", Kind: "application.apply", Attempt: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.StartExecution(ctx, node.ID, session, auth.ID, auth.Digest); err != nil {
			t.Fatal(err)
		}
		if err := store.FinalizeExecution(ctx, node.ID, session, auth.ID); !errors.Is(err, errExecutionAuthorization) {
			t.Fatalf("execution completed without durable result evidence: %v", err)
		}
		if err := store.CheckExecutionStep(ctx, node.ID, session, auth.ID, "apply"); err != nil {
			t.Fatal(err)
		}
		if err := store.StoreExecutionResult(ctx, node.ID, session, auth.ID, json.RawMessage(`{}`), succeeded, false, "", nil); err != nil {
			t.Fatal(err)
		}
		if err := store.CheckExecutionStep(ctx, node.ID, session, auth.ID, "apply"); !errors.Is(err, errExecutionAuthorization) {
			t.Fatalf("execution continued after reporting a result: %v", err)
		}
		if err := store.FinalizeExecution(ctx, node.ID, session, auth.ID); err != nil {
			t.Fatal(err)
		}
		err = store.executionClaimAllowed(ctx, node.ID, session)
		if succeeded && err != nil || !succeeded && !errors.Is(err, errExecutionBlocked) {
			t.Fatalf("terminal fence for succeeded=%v: %v", succeeded, err)
		}
		if err := store.StartExecution(ctx, node.ID, session, auth.ID, auth.Digest); !errors.Is(err, errExecutionAuthorization) {
			t.Fatalf("terminal execution was replayed: %v", err)
		}
		var events int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_events WHERE execution_id=?`, auth.ID).Scan(&events); err != nil || events != 5 {
			t.Fatalf("execution audit missing transitions: %d %v", events, err)
		}
		store.Close()
	}
}
