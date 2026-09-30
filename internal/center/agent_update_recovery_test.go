package center

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/secret"
)

func TestAgentUpdateRecoveryDisposesOnlyConfirmedFailureAtomically(t *testing.T) {
	for _, scenario := range []string{"recover", "stale task", "still running", "unrelated execution", "invalid evidence", "missing confirmation", "abandoned view"} {
		t.Run(scenario, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "recovery-test", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.91", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.91", LANAddress: "10.0.0.91", EnabledKinds: []string{networking.KindLAN}})
			heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)
			now := store.now().UTC().Format(time.RFC3339Nano)
			if _, err := store.db.Exec(`INSERT INTO admins(id,username,password_hash,created_at) VALUES('recovery-admin','recovery-admin','test-hash',?)`, now); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`INSERT INTO agent_updates(id,agent_id,target_version,state,attempt,last_error,created_at,updated_at) VALUES('failed-preview',?,'0.1.0-alpha.88-preview.1','failed',1,'version check failed',?,?)`, node.ID, now, now); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(AgentTask{ID: "failed-preview", Kind: "agent.update", Attempt: 1})
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := secret.Seal(store.key, raw, []byte("execution-task:failed-execution"))
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "invalid evidence" {
				sealed = []byte("invalid")
			}
			state := "failed"
			if scenario == "still running" {
				state = "running"
			}
			if _, err := store.db.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,expires_at,created_at,updated_at) VALUES('failed-execution',?,'failed-preview','agent.update',1,'old-session','digest',?,?,'reported',?,?,?)`, node.ID, sealed, state, now, now, now); err != nil {
				t.Fatal(err)
			}
			if scenario == "unrelated execution" {
				if _, err := store.db.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,expires_at,created_at,updated_at) VALUES('other-execution',?,'other-task','application.apply',1,'other-session','other-digest',X'00','running','execute',?,?,?)`, node.ID, now, now, now); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "abandoned view" {
				before, err := store.ListAgents(ctx)
				if err != nil || len(before) != 1 || before[0].Update == nil || before[0].Update.State != "failed" {
					t.Fatalf("unresolved failure must remain visible: %+v %v", before, err)
				}
				if _, err := store.db.Exec(`UPDATE task_executions SET disposition='abandon' WHERE id='failed-execution'`); err != nil {
					t.Fatal(err)
				}
				after, err := store.ListAgents(ctx)
				if err != nil || len(after) != 1 || after[0].Update != nil {
					t.Fatalf("disposed failure still requires attention: %+v %v", after, err)
				}
				return
			}
			input := AgentUpdateRecoveryInput{FailedUpdateID: "failed-preview", ExecutionStopped: true, Note: "Verified download failed before installation and old execution stopped", adminID: "recovery-admin"}
			if scenario == "stale task" {
				input.FailedUpdateID = "different-task"
			}
			if scenario == "missing confirmation" {
				input.ExecutionStopped = false
			}
			update, err := store.queueAgentUpdate(ctx, node.ID, "0.1.0-alpha.89", &input)
			if scenario == "recover" {
				if err != nil || update.ID == "failed-preview" || update.State != "pending" {
					t.Fatalf("recovery did not queue a new update: %+v %v", update, err)
				}
			} else if err == nil {
				t.Fatal("unsafe recovery was accepted")
			}
			var disposition, originalState, originalError string
			if err := store.db.QueryRow(`SELECT disposition FROM task_executions WHERE id='failed-execution'`).Scan(&disposition); err != nil {
				t.Fatal(err)
			}
			if (disposition == "abandon") != (scenario == "recover") {
				t.Fatalf("recovery did not preserve transaction boundaries: %q", disposition)
			}
			if err := store.db.QueryRow(`SELECT state,last_error FROM agent_updates WHERE id='failed-preview'`).Scan(&originalState, &originalError); err != nil || originalState != "failed" || originalError != "version check failed" {
				t.Fatalf("old failure evidence changed: %q %q %v", originalState, originalError, err)
			}
		})
	}
}
