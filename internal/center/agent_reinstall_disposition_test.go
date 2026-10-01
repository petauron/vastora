package center

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/secret"
)

func TestAgentReinstallRetiredWorkRequiresNewIntent(t *testing.T) {
	for _, retired := range []bool{false, true} {
		for _, kind := range []string{"application.apply", "agent.update"} {
			t.Run(kind+map[bool]string{false: "/same-machine", true: "/retired-machine"}[retired], func(t *testing.T) {
				s, node := reinstallPlanFixture(t)
				ctx := context.Background()
				_ = reviewedReconnectInput(t, s, node.ID) // Creates a synthetic administrator, no recovery mutation.
				id := "saved-deployment"
				stamp := s.now().UTC().Format(time.RFC3339Nano)
				if kind == "application.apply" {
					addReinstallApplication(t, s, node, "saved", meridianAppKey, "0.1.0-alpha.12", "install", "failed")
					if _, err := s.db.Exec(`UPDATE deployments SET attempt=1 WHERE id=?`, id); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := s.db.Exec(`INSERT INTO agent_updates(id,agent_id,target_version,state,attempt,created_at,updated_at) VALUES(?,?,?,'failed',1,?,?)`, id, node.ID, Version, stamp, stamp); err != nil {
						t.Fatal(err)
					}
				}
				raw, _ := json.Marshal(AgentTask{ID: id, Kind: kind, Attempt: 1})
				sealed, err := secret.Seal(s.key, raw, []byte("execution-task:previous-machine-execution"))
				if err != nil {
					t.Fatal(err)
				}
				evidence := []byte("preserved-encrypted-result")
				if _, err = s.db.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,sealed_result,state,phase,expires_at,created_at,updated_at)
 VALUES('previous-machine-execution',?,?,?,1,'previous-machine-session','digest',?,?,'failed','reported',?,?,?)`, node.ID, id, kind, sealed, evidence, stamp, stamp, stamp); err != nil {
					t.Fatal(err)
				}
				if retired {
					tx, err := s.db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					if err = retireAgentExecutionIdentity(ctx, tx, node.ID, stamp); err != nil {
						tx.Rollback()
						t.Fatal(err)
					}
					if err = tx.Commit(); err != nil {
						t.Fatal(err)
					}
				}
				decision := controlplane.ExecutionDisposition{Action: "reexecute", ExecutionStopped: true, Note: "Inspected the stopped previous execution."}
				err = s.ReexecuteExecution(ctx, "previous-machine-execution", "reinstall-review-admin", decision)
				if retired {
					if err == nil || !strings.Contains(err.Error(), "new recovery task") {
						t.Fatalf("retired task was requeued: %v", err)
					}
					if kind == "agent.update" {
						decision.Action = "confirm-completed"
						if err = s.DisposeHelperExecution(ctx, "previous-machine-execution", "reinstall-review-admin", decision); err == nil {
							t.Fatal("replacement version certified an old-machine update")
						}
					}
					var state, disposition string
					var preserved []byte
					if err = s.db.QueryRow(`SELECT state,disposition,sealed_result FROM task_executions WHERE id='previous-machine-execution'`).Scan(&state, &disposition, &preserved); err != nil {
						t.Fatal(err)
					}
					if state != "failed" || disposition != "" || !bytes.Equal(evidence, preserved) {
						t.Fatal("rejected recovery changed historical evidence")
					}
					decision.Action = "abandon"
					if kind == "agent.update" {
						err = s.DisposeHelperExecution(ctx, "previous-machine-execution", "reinstall-review-admin", decision)
					} else {
						err = s.AbandonExecution(ctx, "previous-machine-execution", "reinstall-review-admin", decision)
					}
					if err != nil {
						t.Fatalf("explicit abandonment of historical work was blocked: %v", err)
					}
				} else if err != nil {
					t.Fatalf("same-machine explicit retry was blocked: %v", err)
				}
			})
		}
	}
}
