package center

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/pulse"
	"github.com/petauron/vastora/internal/secret"
)

func monitorRotationFixture(t *testing.T) (*Store, AgentCredential, AgentCredential, AgentReinstallMonitorInput) {
	t.Helper()
	s, service, collector, input := monitorInspectionFixture(t)
	if _, err := s.db.Exec(`UPDATE agents SET capabilities_json=json_set(capabilities_json,'$.pulseRotation',json('true')) WHERE id=?`, service.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueueAgentReinstallMonitorInspection(context.Background(), collector.ID, "reinstall-review-admin", input); err != nil {
		t.Fatal(err)
	}
	task, err := s.claimExecutionTask(context.Background(), service.ID, service.Credential, "monitor-registration-evidence-session", 0)
	if err != nil || task == nil {
		t.Fatalf("inspection: %v", err)
	}
	if response := submitMonitorInspection(t, s, service, task, successfulInspectionJSON(), true); response.Code != http.StatusOK {
		t.Fatalf("inspection: %s", response.Body.String())
	}
	plan, err := s.AgentReinstallPlan(context.Background(), collector.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.PlanRevision = plan.Revision
	return s, service, collector, input
}

func successfulMonitorRotationJSON() json.RawMessage {
	return json.RawMessage(`{"pulseRotation":{"nodeId":"retained-monitor-node","token":"test-rotated-credential-never-publish"}}`)
}

func TestAgentReinstallMonitorRotationKeepsOriginalIdentityAndSealsSecret(t *testing.T) {
	s, service, collector, input := monitorRotationFixture(t)
	ctx := context.Background()
	receipt, err := s.QueueAgentReinstallMonitorRotation(ctx, collector.ID, "reinstall-review-admin", input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.QueueAgentReinstallMonitorRotation(ctx, collector.ID, "reinstall-review-admin", input)
	if err != nil || replay != receipt {
		t.Fatalf("pending replay: %+v %v", replay, err)
	}
	task, err := s.claimExecutionTask(ctx, service.ID, service.Credential, "monitor-registration-evidence-session", 0)
	if err != nil || task == nil || task.PulseRotation == nil || task.ID != receipt.CommandID || task.PulseEnrollment != nil || task.PulseInspection != nil {
		t.Fatalf("rotation task: %+v %v", task, err)
	}
	if task.PulseRotation.NodeID != "retained-monitor-node" || len(task.PulseRotation.Inspection.EnrollmentIDs) != 1 {
		t.Fatal("original node lost")
	}
	if response := submitMonitorInspection(t, s, service, task, successfulMonitorRotationJSON(), true); response.Code != http.StatusOK {
		t.Fatalf("rotation result: %s", response.Body.String())
	}
	plan, err := s.AgentReinstallPlan(ctx, collector.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved := plan.Monitoring[0].Rotation
	if saved == nil || saved.State != "rotated" || saved.RotatedAt == "" || saved.NodeID != task.PulseRotation.NodeID {
		t.Fatalf("receipt: %+v", saved)
	}
	input.PlanRevision = plan.Revision
	replay, err = s.QueueAgentReinstallMonitorRotation(ctx, collector.ID, "reinstall-review-admin", input)
	if err != nil || replay != *saved {
		t.Fatalf("new review rotated again: %+v %v", replay, err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM application_commands WHERE kind=?`, pulse.RotationKind).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate rotation: %d %v", count, err)
	}
	if _, err = s.ClaimNextTask(ctx, collector.ID, collector.Credential); !errors.Is(err, errExecutionBlocked) {
		t.Fatalf("rotation released collector fence: %v", err)
	}
	var commandResult string
	var sealed []byte
	if err = s.db.QueryRow(`SELECT c.result_json,e.sealed_result FROM application_commands c JOIN task_executions e ON e.task_id=c.id WHERE c.id=?`, task.ID).Scan(&commandResult, &sealed); err != nil {
		t.Fatal(err)
	}
	if commandResult != "{}" || bytes.Contains(sealed, []byte("test-rotated-credential")) {
		t.Fatal("plaintext credential leaked")
	}
	plaintext, err := secret.Open(s.key, sealed, []byte("execution-result:"+task.Authorization.ID))
	if err != nil || !bytes.Contains(plaintext, []byte("test-rotated-credential-never-publish")) {
		t.Fatalf("encrypted result lost: %v", err)
	}
	public, _ := json.Marshal(plan)
	if bytes.Contains(public, []byte("test-rotated-credential")) {
		t.Fatal("plan leaked credential")
	}
	var storedReceipt string
	if err = s.db.QueryRow(`SELECT result_json FROM agent_reinstall_monitor_rotations WHERE command_id=?`, task.ID).Scan(&storedReceipt); err != nil || strings.Contains(storedReceipt, "credential-never") {
		t.Fatal("receipt leaked credential")
	}
	dir := s.dataDir
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	after, err := restarted.AgentReinstallPlan(ctx, collector.ID)
	if err != nil || after.Monitoring[0].Rotation == nil || *after.Monitoring[0].Rotation != *saved {
		t.Fatalf("receipt restart: %v", err)
	}
}

func TestAgentReinstallMonitorRotationRequiresReviewAndFreshOriginalIdentity(t *testing.T) {
	for _, mode := range []string{"admin", "operation", "plan", "missing", "ambiguous", "old", "future", "remote-work", "capability"} {
		t.Run(mode, func(t *testing.T) {
			s, service, collector, input := monitorRotationFixture(t)
			admin := "reinstall-review-admin"
			switch mode {
			case "admin":
				admin = "other"
			case "operation":
				input.OperationID = "other"
			case "plan":
				input.PlanRevision = strings.Repeat("f", 64)
			case "missing":
				_, _ = s.db.Exec(`UPDATE agent_reinstall_monitor_inspections SET result_json='{}'`)
			case "ambiguous":
				_, _ = s.db.Exec(`UPDATE agent_reinstall_monitor_inspections SET result_json=json_set(result_json,'$.state','needs_review')`)
			case "old", "future":
				delta := -31 * time.Minute
				if mode == "future" {
					delta = time.Hour
				}
				_, _ = s.db.Exec(`UPDATE agent_reinstall_monitor_inspections SET result_json=json_set(result_json,'$.inspectedAt',?)`, s.now().Add(delta).UTC().Format(time.RFC3339Nano))
			case "remote-work":
				_, _ = s.db.Exec(`UPDATE application_commands SET state='pending',attempt=0 WHERE kind=?`, pulse.InspectionKind)
			case "capability":
				_, _ = s.db.Exec(`UPDATE agents SET capabilities_json=json_remove(capabilities_json,'$.pulseRotation') WHERE id=?`, service.ID)
			}
			if mode != "plan" {
				plan, err := s.AgentReinstallPlan(context.Background(), collector.ID)
				if err != nil {
					t.Fatal(err)
				}
				input.PlanRevision = plan.Revision
			}
			if _, err := s.QueueAgentReinstallMonitorRotation(context.Background(), collector.ID, admin, input); err == nil {
				t.Fatal("unreviewed rotation accepted")
			}
			var count int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM application_commands WHERE kind=?`, pulse.RotationKind).Scan(&count); err != nil || count != 0 {
				t.Fatal("invalid request persisted a rotation")
			}
		})
	}
}

func TestAgentReinstallMonitorRotationRevalidatesEveryBoundary(t *testing.T) {
	for _, stage := range []string{"selection", "authorization", "projection"} {
		for _, mode := range []string{"input", "key", "service", "source", "receipt", "admin", "approval", "attempt"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				s, service, collector, input := monitorRotationFixture(t)
				ctx := context.Background()
				receipt, err := s.QueueAgentReinstallMonitorRotation(ctx, collector.ID, "reinstall-review-admin", input)
				if err != nil {
					t.Fatal(err)
				}
				var task *AgentTask
				if stage != "selection" {
					task, err = s.claimExecutionTask(ctx, service.ID, service.Credential, "monitor-registration-evidence-session", 0)
					if err != nil || task == nil {
						t.Fatalf("claim: %v", err)
					}
				}
				var query string
				var args []any
				switch mode {
				case "input":
					query = `UPDATE application_commands SET input_json=json_set(input_json,'$.task.nodeId','other') WHERE id=?`
					args = []any{receipt.CommandID}
				case "key":
					query = `UPDATE agents SET x25519_public_key=? WHERE id=?`
					args = []any{testAgentPublicKey(t), collector.ID}
				case "service":
					query = `UPDATE deployments SET state='failed' WHERE agent_id=?`
					args = []any{service.ID}
				case "source":
					query = `UPDATE deployments SET config_json=json_set(config_json,'$.node_name','changed') WHERE application_id=?`
					args = []any{input.ApplicationID}
				case "receipt":
					query = `UPDATE agent_reinstall_monitor_inspections SET result_json=json_set(result_json,'$.nodeId','other')`
				case "admin":
					query = `DELETE FROM admins WHERE id='reinstall-review-admin'`
				case "approval":
					query = `DELETE FROM agent_reinstall_monitor_rotations WHERE command_id=?`
					args = []any{receipt.CommandID}
				case "attempt":
					query = `UPDATE application_commands SET attempt=2 WHERE id=?`
					args = []any{receipt.CommandID}
				}
				if _, err = s.db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
				if stage == "selection" {
					selected, _ := s.claimExecutionTask(ctx, service.ID, service.Credential, "monitor-registration-evidence-session", 0)
					if selected != nil && selected.ID == receipt.CommandID {
						t.Fatal("changed authority selected")
					}
				} else {
					tx, err := s.db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					if stage == "authorization" {
						_, err = s.persistExecutionAuthorization(ctx, tx, service.ID, "monitor-registration-evidence-session", *task)
					} else {
						err = s.projectApplicationCommand(ctx, tx, func(tx *sql.Tx) error { return tx.Commit() }, service.ID, task.ID, task.Attempt, true, "", successfulMonitorRotationJSON(), false)
					}
					_ = tx.Rollback()
					if err == nil {
						t.Fatal("changed authority accepted")
					}
				}
			})
		}
	}
}

func TestAgentReinstallMonitorRotationNeverReplaysFailedOrUnknownWrites(t *testing.T) {
	for _, mode := range []string{"failed", "missing-result", "wrong-node", "bad-token", "expired", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, service, collector, input := monitorRotationFixture(t)
			ctx := context.Background()
			receipt, err := s.QueueAgentReinstallMonitorRotation(ctx, collector.ID, "reinstall-review-admin", input)
			if err != nil {
				t.Fatal(err)
			}
			task, err := s.claimExecutionTask(ctx, service.ID, service.Credential, "monitor-registration-evidence-session", 0)
			if err != nil || task == nil {
				t.Fatalf("claim: %v", err)
			}
			if mode == "expired" || mode == "unknown" {
				if mode == "expired" {
					_, err = s.db.Exec(`UPDATE application_commands SET lease_expires_at='2020-01-01T00:00:00Z' WHERE id=?`, task.ID)
				} else {
					_, err = s.db.Exec(`UPDATE task_executions SET state='unknown' WHERE id=?`, task.Authorization.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
			} else {
				raw := successfulMonitorRotationJSON()
				if mode == "missing-result" {
					raw = json.RawMessage(`{}`)
				}
				if mode == "wrong-node" {
					raw = bytes.ReplaceAll(raw, []byte("retained-monitor-node"), []byte("other"))
				}
				if mode == "bad-token" {
					raw = bytes.ReplaceAll(raw, []byte("test-rotated-credential-never-publish"), []byte("short"))
				}
				response := submitMonitorInspection(t, s, service, task, raw, mode != "failed")
				if mode == "failed" && response.Code != http.StatusOK {
					t.Fatalf("failed result: %s", response.Body.String())
				}
				if mode != "failed" && response.Code == http.StatusOK {
					t.Fatal("invalid result accepted")
				}
			}
			plan, err := s.AgentReinstallPlan(ctx, collector.ID)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Monitoring[0].Rotation == nil || plan.Monitoring[0].Rotation.State != "needs_review" {
				t.Fatalf("outcome hidden: %+v", plan.Monitoring[0].Rotation)
			}
			input.PlanRevision = plan.Revision
			replay, err := s.QueueAgentReinstallMonitorRotation(ctx, collector.ID, "reinstall-review-admin", input)
			if err != nil || replay.CommandID != receipt.CommandID {
				t.Fatalf("uncertain operation not retained: %+v %v", replay, err)
			}
			if next, _ := s.claimExecutionTask(ctx, service.ID, service.Credential, "monitor-registration-evidence-session", 0); next != nil && next.PulseRotation != nil {
				t.Fatal("rotation replayed")
			}
		})
	}
}
