package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAgentReinstallMonitorReportingRetainsSampleAge(t *testing.T) {
	for _, mode := range []string{"fresh", "expired", "delayed-receipt", "future-receipt", "future-authorization", "missing-proof", "corrupt-proof"} {
		t.Run(mode, func(t *testing.T) {
			s, service, node, input := monitorReportingFixture(t)
			ctx := context.Background()
			receipt, err := s.QueueAgentReinstallMonitorReporting(ctx, node.ID, "reinstall-review-admin", input)
			if err != nil {
				t.Fatal(err)
			}
			task, err := s.claimExecutionTask(ctx, service.ID, service.Credential, "monitor-registration-evidence-session", 0)
			if err != nil || task == nil || task.PulseReporting == nil {
				t.Fatalf("report claim: %v", err)
			}
			// The Service observed a sample already nine minutes old. Its
			// receipt must retain only one minute of freshness, not ten.
			raw := json.RawMessage(`{"pulseReporting":{"node_id":"11111111-1111-4111-8111-111111111111","observed_at_unix_ms":542000,"rotated_at_unix_ms":1000,"last_seen_at_unix_ms":2000}}`)
			if response := submitMonitorInspection(t, s, service, task, raw, true); response.Code != http.StatusOK {
				t.Fatal(response.Body.String())
			}
			checked := s.now().Add(-30 * time.Second)
			want := "verified"
			switch mode {
			case "expired":
				checked, want = s.now().Add(-61*time.Second), "stale"
			case "delayed-receipt":
				checked, want = s.now(), "stale"
			case "future-receipt":
				checked, want = s.now().Add(time.Minute), "stale"
			case "missing-proof":
				_, err = s.db.Exec(`UPDATE task_executions SET phase='result_received' WHERE task_id=?`, task.ID)
				want = "needs_review"
			case "corrupt-proof":
				_, err = s.db.Exec(`UPDATE task_executions SET sealed_result=X'1234' WHERE task_id=?`, task.ID)
				want = "needs_review"
			}
			if err != nil {
				t.Fatal(err)
			}
			created := checked.Add(-time.Second)
			if mode == "delayed-receipt" {
				created = checked.Add(-61 * time.Second)
			} else if mode == "future-authorization" {
				created, want = checked.Add(time.Second), "needs_review"
			}
			if _, err = s.db.Exec(`UPDATE task_executions SET created_at=? WHERE task_id=?`, created.UTC().Format(time.RFC3339Nano), task.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(`UPDATE agent_reinstall_monitor_reports SET result_json=json_set(result_json,'$.checkedAt',?) WHERE command_id=?`, checked.UTC().Format(time.RFC3339Nano), receipt.CommandID); err != nil {
				t.Fatal(err)
			}
			plan, err := s.AgentReinstallPlan(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := plan.Monitoring[0].Reporting; got == nil || got.State != want {
				t.Fatalf("sample age lost: got %+v, want %s", got, want)
			}
			if blocked, err := agentReinstallBlocked(ctx, s.db, node.ID); err != nil || !blocked {
				t.Fatal("report freshness released the business fence")
			}
		})
	}
}

func monitorReportingFixture(t *testing.T) (*Store, AgentCredential, AgentCredential, AgentReinstallMonitorInput) {
	t.Helper()
	s, service, node, input := monitorRestoreFixture(t)
	ctx := context.Background()
	if _, err := s.QueueAgentReinstallMonitorRestore(ctx, node.ID, "reinstall-review-admin", input); err != nil {
		t.Fatal(err)
	}
	task, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "monitor-original-restore-session", 0)
	if err != nil || task == nil {
		t.Fatalf("restore claim: %v", err)
	}
	response := monitorRestoreResult(t, s, node, task, json.RawMessage(`{"services":[],"pulseRestored":{"nodeId":"11111111-1111-4111-8111-111111111111"}}`))
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	if _, err = s.db.Exec(`UPDATE agents SET capabilities_json=json_set(capabilities_json,'$.pulseReporting',json('true')) WHERE id=?`, service.ID); err != nil {
		t.Fatal(err)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.PlanRevision = plan.Revision
	return s, service, node, input
}

func TestAgentReinstallMonitorReportingActualExecutionAndReceipt(t *testing.T) {
	for _, mode := range []string{"fresh", "retained", "missing", "future", "wrong-node"} {
		t.Run(mode, func(t *testing.T) {
			s, service, node, input := monitorReportingFixture(t)
			ctx := context.Background()
			receipt, err := s.QueueAgentReinstallMonitorReporting(ctx, node.ID, "reinstall-review-admin", input)
			if err != nil {
				t.Fatal(err)
			}
			var stored string
			if err = s.db.QueryRow(`SELECT input_json FROM application_commands WHERE id=?`, receipt.CommandID).Scan(&stored); err != nil || strings.Contains(stored, "credential-never") {
				t.Fatal("credential leaked into command metadata")
			}
			task, err := s.claimExecutionTask(ctx, service.ID, service.Credential, "monitor-registration-evidence-session", 0)
			if err != nil || task == nil || task.PulseReporting == nil || task.ID != receipt.CommandID {
				t.Fatalf("report claim: %v", err)
			}
			if task.PulseReporting.Credentials.Token != "test-rotated-credential-never-publish" {
				t.Fatal("did not use the original rotated credential")
			}
			raw := `{"pulseReporting":{"node_id":"11111111-1111-4111-8111-111111111111","observed_at_unix_ms":3000,"rotated_at_unix_ms":1000,"last_seen_at_unix_ms":2000}}`
			switch mode {
			case "retained":
				raw = strings.ReplaceAll(raw, ":2000", ":999")
			case "missing":
				raw = strings.ReplaceAll(raw, ":2000", ":null")
			case "future":
				raw = strings.ReplaceAll(raw, ":2000", ":3001")
			case "wrong-node":
				raw = strings.ReplaceAll(raw, "11111111-1111-4111-8111-111111111111", "other")
			}
			response := submitMonitorInspection(t, s, service, task, json.RawMessage(raw), true)
			if mode == "future" || mode == "wrong-node" {
				if response.Code == http.StatusOK {
					t.Fatal("invalid report accepted")
				}
				return
			}
			if response.Code != http.StatusOK {
				t.Fatal(response.Body.String())
			}
			plan, err := s.AgentReinstallPlan(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			saved := plan.Monitoring[0].Reporting
			want := "not_reporting"
			if mode == "fresh" {
				want = "verified"
			}
			if saved == nil || saved.State != want || saved.CheckedAt == "" {
				t.Fatalf("receipt: %+v", saved)
			}
			if blocked, err := agentReinstallBlocked(ctx, s.db, node.ID); err != nil || !blocked {
				t.Fatal("monitoring read released the business fence")
			}
			input.PlanRevision = plan.Revision
			next, err := s.QueueAgentReinstallMonitorReporting(ctx, node.ID, "reinstall-review-admin", input)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "fresh" {
				if next.CommandID != receipt.CommandID {
					t.Fatal("verified response replay issued another read")
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
				after, err := restarted.AgentReinstallPlan(ctx, node.ID)
				if err != nil || after.Monitoring[0].Reporting == nil || after.Monitoring[0].Reporting.State != "verified" {
					t.Fatalf("restart lost verified receipt: %v", err)
				}
			} else if next.CommandID == receipt.CommandID {
				t.Fatal("explicit retry reused the retained sample receipt")
			}
		})
	}
}

func TestAgentReinstallMonitorReportingRevalidatesSealingAndProjection(t *testing.T) {
	for _, stage := range []string{"authorization", "projection"} {
		t.Run(stage, func(t *testing.T) {
			s, service, node, input := monitorReportingFixture(t)
			ctx := context.Background()
			_, err := s.QueueAgentReinstallMonitorReporting(ctx, node.ID, "reinstall-review-admin", input)
			if err != nil {
				t.Fatal(err)
			}
			task, err := s.claimExecutionTask(ctx, service.ID, service.Credential, "monitor-registration-evidence-session", 0)
			if err != nil || task == nil {
				t.Fatalf("claim: %v", err)
			}
			_, err = s.db.Exec(`DELETE FROM agent_reinstall_network_approvals`)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "authorization" {
				tx, err := s.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				if _, err = s.persistExecutionAuthorization(ctx, tx, service.ID, "monitor-registration-evidence-session", *task); err == nil {
					t.Fatal("changed network authorized execution")
				}
			} else {
				tx, err := s.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				err = s.projectApplicationCommand(ctx, tx, func(tx *sql.Tx) error { return tx.Commit() }, service.ID, task.ID, task.Attempt, true, "", json.RawMessage(`{"pulseReporting":{"node_id":"11111111-1111-4111-8111-111111111111","observed_at_unix_ms":3000,"rotated_at_unix_ms":1000,"last_seen_at_unix_ms":2000}}`), false)
				if err == nil {
					t.Fatal("changed network accepted reporting result")
				}
			}
		})
	}
}

func TestAgentReinstallMonitorReportingRejectsChangedAuthority(t *testing.T) {
	for _, mode := range []string{"restore-proof", "network", "capability", "service", "key"} {
		t.Run(mode, func(t *testing.T) {
			s, service, node, input := monitorReportingFixture(t)
			_, err := s.QueueAgentReinstallMonitorReporting(context.Background(), node.ID, "reinstall-review-admin", input)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "restore-proof":
				_, err = s.db.Exec(`UPDATE task_executions SET sealed_result=X'1234' WHERE task_id LIKE 'reinstall-monitor-collector-%'`)
			case "network":
				_, err = s.db.Exec(`DELETE FROM agent_reinstall_network_approvals`)
			case "capability":
				_, err = s.db.Exec(`UPDATE agents SET capabilities_json=json_remove(capabilities_json,'$.pulseReporting') WHERE id=?`, service.ID)
			case "service":
				_, err = s.db.Exec(`UPDATE applications SET status='stopped' WHERE id=(SELECT application_id FROM application_commands WHERE kind='pulse.node.reporting')`)
			case "key":
				_, err = s.db.Exec(`UPDATE agents SET x25519_public_key=? WHERE id=?`, testAgentPublicKey(t), node.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			task, _ := s.claimExecutionTask(context.Background(), service.ID, service.Credential, "monitor-registration-evidence-session", 0)
			if task != nil && task.PulseReporting != nil {
				t.Fatal("reporting task issued after authority changed")
			}
		})
	}
}
