package center

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/secret"
)

func TestAbandonOrphanCommandReleasesPulseEnrollment(t *testing.T) {
	for _, mode := range []string{"orphan", "changed-attempt", "reexecute", "not-admin", "not-stopped", "audit-failure", "invalid-evidence"} {
		t.Run(mode, func(t *testing.T) {
			store, service, collector, deployment := pulseFixture(t)
			ctx := context.Background()
			addPulsePrivateFixture(t, store, deployment.ApplicationID, service.ID)
			if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: collector.ID, AppKey: pulseAgentAppKey, Config: json.RawMessage(`{"node_region":"SG","geoip_provider":"disabled"}`)}); err != nil {
				t.Fatal(err)
			}
			session := "orphan-command-current-process-session"
			if err := store.RegisterExecutionSession(ctx, service.ID, service.Credential, session, controlplane.ExecutionProtocol); err != nil {
				t.Fatal(err)
			}
			taskID := "removed-application-command"
			if mode == "changed-attempt" {
				if err := store.db.QueryRow(`SELECT id FROM application_commands WHERE agent_id=? AND state='pending'`, service.ID).Scan(&taskID); err != nil {
					t.Fatal(err)
				}
			}
			const executionID = "orphan-command-execution"
			raw, err := json.Marshal(AgentTask{ID: taskID, Kind: "application.command", Attempt: 1})
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := secret.Seal(store.key, raw, []byte("execution-task:"+executionID))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "invalid-evidence" {
				sealed = []byte("invalid")
			}
			stamp := store.now().UTC().Format(time.RFC3339Nano)
			if _, err := store.db.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,sealed_result,state,phase,last_error,expires_at,created_at,updated_at) VALUES(?,?,?,'application.command',1,?,'digest',?,X'1234','failed','reported','preserved failure',?,?,?)`, executionID, service.ID, taskID, session, sealed, stamp, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if task, err := store.claimExecutionTask(ctx, service.ID, service.Credential, session, 0); !errors.Is(err, errExecutionBlocked) || task != nil {
				t.Fatalf("unresolved execution did not block enrollment: %v", err)
			}
			cookie, _, err := store.CreateFirstAdmin(ctx, "orphan-command-admin", "test-only-strong-password")
			if err != nil {
				t.Fatal(err)
			}
			adminID, err := store.SessionAdminID(ctx, cookie)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "not-admin" {
				adminID = "missing-admin"
			}
			if mode == "audit-failure" {
				if _, err := store.db.Exec(`CREATE TRIGGER reject_orphan_abandon BEFORE UPDATE ON task_executions WHEN NEW.disposition='abandon' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
					t.Fatal(err)
				}
			}
			decision := controlplane.ExecutionDisposition{Action: "abandon", ExecutionStopped: mode != "not-stopped", Note: "Verified the removed application command stopped; retain execution evidence."}
			if mode == "reexecute" {
				decision.Action = "reexecute"
				err = store.ReexecuteExecution(ctx, executionID, adminID, decision)
			} else {
				err = store.AbandonExecution(ctx, executionID, adminID, decision)
			}
			if (err == nil) != (mode == "orphan") {
				t.Fatalf("unexpected disposition outcome: %v", err)
			}
			var disposition, state, failure string
			var after, result []byte
			if err := store.db.QueryRow(`SELECT disposition,state,last_error,sealed_task,sealed_result FROM task_executions WHERE id=?`, executionID).Scan(&disposition, &state, &failure, &after, &result); err != nil {
				t.Fatal(err)
			}
			if state != "failed" || failure != "preserved failure" || !bytes.Equal(after, sealed) || !bytes.Equal(result, []byte{0x12, 0x34}) {
				t.Fatal("retained execution evidence changed")
			}
			if mode != "orphan" {
				if disposition != "" {
					t.Fatal("rejected operation released the execution fence")
				}
				return
			}
			if disposition != "abandon" {
				t.Fatal("orphan was not abandoned")
			}
			if err := store.AbandonExecution(ctx, executionID, adminID, decision); err == nil {
				t.Fatal("duplicate abandonment accepted")
			}
			next, err := store.claimExecutionTask(ctx, service.ID, service.Credential, session, 0)
			if err != nil || next == nil || next.PulseEnrollment == nil {
				t.Fatalf("Pulse enrollment remained blocked: %v", err)
			}
		})
	}
}
