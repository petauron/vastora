package center

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/pulse"
)

func TestAgentReinstallFencesRemotePulseEnrollment(t *testing.T) {
	for _, phase := range []string{"pending", "offered", "running", "result_received", "pause-failure"} {
		t.Run(phase, func(t *testing.T) {
			store, monitor, collector, deployment := pulseFixture(t)
			ctx := context.Background()
			addPulsePrivateFixture(t, store, deployment.ApplicationID, monitor.ID)
			install, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: collector.ID, AppKey: pulseAgentAppKey})
			if err != nil {
				t.Fatal(err)
			}
			session := "monitor-current-process-session"
			if err = store.RegisterExecutionSession(ctx, monitor.ID, monitor.Credential, session, controlplane.ExecutionProtocol); err != nil {
				t.Fatal(err)
			}
			var commandID string
			if err = store.db.QueryRow(`SELECT id FROM application_commands WHERE gateway_node_id=? AND state='pending'`, collector.ID).Scan(&commandID); err != nil {
				t.Fatal(err)
			}
			var task *AgentTask
			var originalEvidence []byte
			if phase != "pending" {
				task, err = store.claimExecutionTask(ctx, monitor.ID, monitor.Credential, session, 0)
				if err != nil || task == nil || task.PulseEnrollment == nil {
					t.Fatalf("could not claim initial remote command: %+v %v", task, err)
				}
				if phase != "offered" {
					if err = store.StartExecution(ctx, monitor.ID, session, task.Authorization.ID, task.Authorization.Digest); err != nil {
						t.Fatal(err)
					}
				}
				if phase == "result_received" {
					raw, _ := json.Marshal(map[string]any{"pulseEnrollment": pulse.EnrollmentResult{ID: "old-collector-enrollment", Token: strings.Repeat("e", 32), ExpiresAtUnixMS: store.now().Add(time.Hour).UnixMilli()}})
					if err = store.StoreExecutionResult(ctx, monitor.ID, session, task.Authorization.ID, raw, true, false, "", nil, false); err != nil {
						t.Fatal(err)
					}
				}
				if err = store.db.QueryRow(`SELECT sealed_result FROM task_executions WHERE id=?`, task.Authorization.ID).Scan(&originalEvidence); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = store.db.Exec(`UPDATE agents SET last_seen_at='2020-01-01T00:00:00Z' WHERE id=?`, collector.ID); err != nil {
				t.Fatal(err)
			}
			if phase == "pause-failure" {
				if _, err = store.db.Exec(`CREATE TRIGGER reject_remote_pause BEFORE UPDATE ON task_executions WHEN NEW.state='unknown' BEGIN SELECT RAISE(ABORT,'pause audit unavailable'); END`); err != nil {
					t.Fatal(err)
				}
			}
			_, err = store.beginAgentReinstall(ctx, collector.ID, "reinstall-review-admin", reviewedReconnectInput(t, store, collector.ID))
			if phase == "pause-failure" {
				if err == nil {
					t.Fatal("partial remote pause committed")
				}
				if err = store.authenticateAgent(ctx, collector.ID, collector.Credential); err != nil {
					t.Fatalf("failed reservation revoked collector: %v", err)
				}
				var count int
				if err = store.db.QueryRow(`SELECT COUNT(*) FROM agent_reinstall_operations`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("partial operation survived: %d %v", count, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = store.authenticateAgent(ctx, monitor.ID, monitor.Credential); err != nil {
				t.Fatalf("remote host identity was revoked: %v", err)
			}
			// Final issuance and projection recheck the target even when an earlier
			// selection or an already received result predates the recovery.
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.persistExecutionAuthorization(ctx, tx, monitor.ID, session, AgentTask{ID: commandID, Kind: "application.command", Attempt: 2})
			if !errors.Is(err, errExecutionBlocked) {
				t.Fatalf("remote authorization bypassed target fence: %v", err)
			}
			err = store.projectApplicationCommand(ctx, tx, func(*sql.Tx) error { t.Error("unexpected projection commit"); return nil }, monitor.ID, commandID, 1, true, "", json.RawMessage(`{}`), false)
			if !errors.Is(err, errExecutionBlocked) {
				t.Fatalf("remote result bypassed target fence: %v", err)
			}
			tx.Rollback()
			if phase == "pending" {
				if next, err := store.claimExecutionTask(ctx, monitor.ID, monitor.Credential, session, 0); err != nil || next != nil {
					t.Fatalf("old unclaimed command was dispatched: %+v %v", next, err)
				}
				var attempt int
				if err = store.db.QueryRow(`SELECT attempt FROM application_commands WHERE id=?`, commandID).Scan(&attempt); err != nil || attempt != 0 {
					t.Fatalf("paused command consumed an attempt: %d %v", attempt, err)
				}
				return
			}
			if err = store.StartExecution(ctx, monitor.ID, session, task.Authorization.ID, task.Authorization.Digest); !errors.Is(err, errExecutionAuthorization) {
				t.Fatalf("old offer could start: %v", err)
			}
			if err = store.CheckExecutionStep(ctx, monitor.ID, session, task.Authorization.ID, "apply"); !errors.Is(err, errExecutionAuthorization) {
				t.Fatalf("old execution could continue: %v", err)
			}
			if err = store.RenewExecution(ctx, monitor.ID, session, task.Authorization.ID); !errors.Is(err, errExecutionAuthorization) {
				t.Fatalf("old execution could renew: %v", err)
			}
			decision := controlplane.ExecutionDisposition{Action: "reexecute", ExecutionStopped: true, Note: "Inspected the related monitor command."}
			if err = store.ReexecuteExecution(ctx, task.Authorization.ID, "reinstall-review-admin", decision); !errors.Is(err, errExecutionBlocked) {
				t.Fatalf("old remote command could be replayed: %v", err)
			}
			if phase == "result_received" {
				store.recoverReceivedExecutionResults(ctx, monitor.ID)
				decision.Action = "confirm-completed"
				if err = store.ConfirmExecution(ctx, task.Authorization.ID, "reinstall-review-admin", decision); !errors.Is(err, errExecutionBlocked) {
					t.Fatalf("old remote result confirmed for replacement: %v", err)
				}
			}
			var state, retired string
			var evidence []byte
			if err = store.db.QueryRow(`SELECT state,identity_retired_at,sealed_result FROM task_executions WHERE id=?`, task.Authorization.ID).Scan(&state, &retired, &evidence); err != nil || state != "unknown" || retired != "" || !bytes.Equal(evidence, originalEvidence) {
				t.Fatalf("remote evidence changed: state=%q retired=%q err=%v", state, retired, err)
			}
			var secret sql.NullString
			if err = store.db.QueryRow(`SELECT secret_id FROM deployments WHERE id=?`, install.ID).Scan(&secret); err != nil || secret.Valid {
				t.Fatalf("old monitor enrollment projected: %v", err)
			}
			decision.Action = "abandon"
			if err = store.AbandonExecution(ctx, task.Authorization.ID, "reinstall-review-admin", decision); err != nil {
				t.Fatalf("inspected remote command cannot be abandoned: %v", err)
			}
		})
	}
}
