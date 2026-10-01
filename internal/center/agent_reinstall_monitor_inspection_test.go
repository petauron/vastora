package center

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/pulse"
)

func monitorInspectionFixture(t *testing.T) (*Store, AgentCredential, AgentCredential, AgentReinstallMonitorInput) {
	t.Helper()
	s, service, collector, install, _ := reinstallMonitorFixture(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?),(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, agentConnectionModeSetting, "lan", agentConnectURLSetting, "https://center.example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE agents SET last_seen_at='2020-01-01T00:00:00Z', roles_json='["worker"]', capabilities_json=json_set(capabilities_json,'$.docker',json('true')) WHERE id=?`, collector.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE agents SET capabilities_json=json_set(capabilities_json,'$.pulseInspection',json('true')) WHERE id=?`, service.ID); err != nil {
		t.Fatal(err)
	}
	enrollment, err := createReviewedReconnect(t, s, ctx, collector.ID)
	if err != nil {
		t.Fatal(err)
	}
	collector, err = s.EnrollAgent(ctx, enrollment.Token, Version, "linux", "amd64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.AgentReinstallPlan(ctx, collector.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, service, collector, AgentReinstallMonitorInput{OperationID: plan.Recovery.ID, PlanRevision: plan.Revision, ApplicationID: install.ApplicationID}
}

func submitMonitorInspection(t *testing.T, s *Store, service AgentCredential, task *AgentTask, result json.RawMessage, succeeded bool) *httptest.ResponseRecorder {
	t.Helper()
	const session = "monitor-registration-evidence-session"
	if err := s.StartExecution(context.Background(), service.ID, session, task.Authorization.ID, task.Authorization.Digest); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"executionId": task.Authorization.ID, "sessionId": session, "attempt": task.Attempt, "succeeded": succeeded, "result": result})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+service.ID+"/tasks/"+task.ID+"/result", bytes.NewReader(input))
	request.Header.Set("Authorization", "Bearer "+service.Credential)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewServer(s, "", false).Handler().ServeHTTP(response, request)
	return response
}

func successfulInspectionJSON() json.RawMessage {
	return json.RawMessage(`{"pulseInspection":{"records":[{"id":"original-registration","expires_at_unix_ms":100,"consumed_at_unix_ms":90,"node_id":"retained-monitor-node","node_active":true}]}}`)
}

func TestAgentReinstallMonitorInspectionReadOnlyRoundTrip(t *testing.T) {
	s, service, collector, input := monitorInspectionFixture(t)
	ctx := context.Background()
	for _, mode := range []string{"admin", "plan", "operation"} {
		bad := input
		admin := "reinstall-review-admin"
		switch mode {
		case "admin":
			admin = "other"
		case "plan":
			bad.PlanRevision = strings.Repeat("f", 64)
		case "operation":
			bad.OperationID = "other"
		}
		if _, err := s.QueueAgentReinstallMonitorInspection(ctx, collector.ID, admin, bad); err == nil {
			t.Fatalf("accepted unreviewed %s", mode)
		}
	}
	receipt, err := s.QueueAgentReinstallMonitorInspection(ctx, collector.ID, "reinstall-review-admin", input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.QueueAgentReinstallMonitorInspection(ctx, collector.ID, "reinstall-review-admin", input)
	if err != nil || replay != receipt {
		t.Fatalf("lost response duplicated read: %+v %v", replay, err)
	}
	task, err := s.claimExecutionTask(ctx, service.ID, service.Credential, "monitor-registration-evidence-session", 0)
	if err != nil || task == nil || task.ID != receipt.CommandID || task.PulseInspection == nil {
		t.Fatalf("approved inspection blocked: %+v %v", task, err)
	}
	if task.PulseEnrollment != nil || task.PulseInspection.ApplicationID == input.ApplicationID {
		t.Fatal("inspection became collector installation or enrollment")
	}
	response := submitMonitorInspection(t, s, service, task, successfulInspectionJSON(), true)
	if response.Code != http.StatusOK {
		t.Fatalf("result: %d %s", response.Code, response.Body.String())
	}
	plan, err := s.AgentReinstallPlan(ctx, collector.ID)
	if err != nil || len(plan.Monitoring) != 1 || plan.Monitoring[0].Inspection == nil {
		t.Fatalf("receipt missing: %+v %v", plan.Monitoring, err)
	}
	got := plan.Monitoring[0].Inspection
	if got.State != "verified" || got.NodeID != "retained-monitor-node" || got.InspectedAt == "" {
		t.Fatalf("identity not retained: %+v", got)
	}
	replay, err = s.QueueAgentReinstallMonitorInspection(ctx, collector.ID, "reinstall-review-admin", input)
	if err != nil || replay != *got {
		t.Fatalf("terminal receipt lost: %+v %v", replay, err)
	}
	if _, err = s.ClaimNextTask(ctx, collector.ID, collector.Credential); !errors.Is(err, errExecutionBlocked) {
		t.Fatalf("inspection released recovery fence: %v", err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM application_commands WHERE kind=?`, pulse.InspectionKind).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate task: %d %v", count, err)
	}
	var state string
	if err = s.db.QueryRow(`SELECT state FROM agent_reinstall_operations WHERE id=?`, input.OperationID).Scan(&state); err != nil || state != "review_required" {
		t.Fatal("inspection marked business recovery complete")
	}
	directory := s.dataDir
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reloaded, err := reopened.AgentReinstallPlan(ctx, collector.ID)
	if err != nil || reloaded.Monitoring[0].Inspection == nil || *reloaded.Monitoring[0].Inspection != *got {
		t.Fatalf("receipt did not survive restart: %v", err)
	}
	raw, _ := json.Marshal(plan)
	if bytes.Contains(raw, []byte("test-secret-")) {
		t.Fatal("inspection leaked enrollment credential")
	}
}

func TestAgentReinstallMonitorInspectionRejectsChangedAuthority(t *testing.T) {
	for _, stage := range []string{"selection", "authorization", "result"} {
		for _, mode := range []string{"input", "replacement", "admin", "source", "service-version", "collector-uninstall", "command-service"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				s, service, collector, input := monitorInspectionFixture(t)
				ctx := context.Background()
				receipt, err := s.QueueAgentReinstallMonitorInspection(ctx, collector.ID, "reinstall-review-admin", input)
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
				case "command-service":
					query = `UPDATE application_commands SET application_id=? WHERE id=?`
					args = []any{input.ApplicationID, receipt.CommandID}
				case "input":
					query = `UPDATE application_commands SET input_json=json_set(input_json,'$.task.enrollmentIds[0]','different') WHERE id=?`
					args = []any{receipt.CommandID}
				case "replacement":
					query = `UPDATE agents SET x25519_public_key=? WHERE id=?`
					args = []any{testAgentPublicKey(t), collector.ID}
				case "admin":
					query = `DELETE FROM admins WHERE id='reinstall-review-admin'`
				case "source":
					query = `UPDATE task_executions SET sealed_result=X'1234' WHERE task_id IN (SELECT id FROM application_commands WHERE kind='pulse.enrollment.create')`
				case "service-version":
					query = `UPDATE deployments SET state='failed' WHERE application_id=(SELECT application_id FROM application_commands WHERE id=?)`
					args = []any{receipt.CommandID}
				case "collector-uninstall":
					query = `UPDATE deployments SET operation='uninstall' WHERE application_id=?`
					args = []any{input.ApplicationID}
				}
				if _, err = s.db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
				switch stage {
				case "selection":
					claimed, claimErr := s.claimExecutionTask(ctx, service.ID, service.Credential, "monitor-registration-evidence-session", 0)
					if claimed != nil && claimed.ID == receipt.CommandID {
						t.Fatalf("changed command selected: %v", claimErr)
					}
				case "authorization":
					tx, err := s.db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					_, err = s.persistExecutionAuthorization(ctx, tx, service.ID, "monitor-registration-evidence-session", *task)
					_ = tx.Rollback()
					if err == nil {
						t.Fatal("changed binding authorized")
					}
				case "result":
					// Direct projection check: even an execution already authorized before
					// the change cannot install a usable identity receipt afterward.
					tx, err := s.db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					err = s.projectApplicationCommand(ctx, tx, func(tx *sql.Tx) error { return tx.Commit() }, service.ID, task.ID, task.Attempt, true, "", successfulInspectionJSON(), false)
					_ = tx.Rollback()
					if err == nil {
						t.Fatal("changed binding projected")
					}
				}
				if _, err = s.QueueAgentReinstallMonitorInspection(ctx, collector.ID, "reinstall-review-admin", input); err == nil {
					t.Fatal("stale receipt replay accepted")
				}
				var raw string
				if err = s.db.QueryRow(`SELECT result_json FROM agent_reinstall_monitor_inspections WHERE command_id=?`, receipt.CommandID).Scan(&raw); err != nil || raw != "{}" {
					t.Fatalf("changed binding produced receipt: %s %v", raw, err)
				}
			})
		}
	}
}

func TestAgentReinstallMonitorInspectionResultDoesNotGuessIdentity(t *testing.T) {
	for _, mode := range []string{"failed", "missing", "partial", "inactive", "unconsumed"} {
		t.Run(mode, func(t *testing.T) {
			s, service, collector, input := monitorInspectionFixture(t)
			ctx := context.Background()
			_, err := s.QueueAgentReinstallMonitorInspection(ctx, collector.ID, "reinstall-review-admin", input)
			if err != nil {
				t.Fatal(err)
			}
			task, err := s.claimExecutionTask(ctx, service.ID, service.Credential, "monitor-registration-evidence-session", 0)
			if err != nil || task == nil {
				t.Fatalf("claim: %v", err)
			}
			raw := successfulInspectionJSON()
			switch mode {
			case "failed", "missing":
				raw = json.RawMessage(`{}`)
			case "partial":
				raw = json.RawMessage(`{"pulseInspection":{"records":[]}}`)
			case "inactive":
				raw = bytes.ReplaceAll(raw, []byte(`"node_active":true`), []byte(`"node_active":false`))
			case "unconsumed":
				raw = json.RawMessage(`{"pulseInspection":{"records":[{"id":"original-registration","expires_at_unix_ms":100,"consumed_at_unix_ms":null,"node_id":null,"node_active":false}]}}`)
			}
			response := submitMonitorInspection(t, s, service, task, raw, mode != "failed")
			plan, err := s.AgentReinstallPlan(ctx, collector.ID)
			if err != nil {
				t.Fatal(err)
			}
			got := plan.Monitoring[0].Inspection
			if got == nil || got.State == "verified" || got.NodeID != "" {
				t.Fatalf("guessed identity: %+v HTTP%d", got, response.Code)
			}
			if mode == "inactive" || mode == "unconsumed" {
				if response.Code != http.StatusOK || got.State != "needs_review" {
					t.Fatalf("completed ambiguous read not retained: %+v", got)
				}
			}
			if mode == "failed" && got.State != "failed" {
				t.Fatalf("failed read: %+v", got)
			}
		})
	}
}

func TestAgentReinstallMonitorInspectionRequiresDurableApproval(t *testing.T) {
	s, service, collector, input := monitorInspectionFixture(t)
	ctx := context.Background()
	receipt, err := s.QueueAgentReinstallMonitorInspection(ctx, collector.ID, "reinstall-review-admin", input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DELETE FROM agent_reinstall_monitor_inspections WHERE command_id=?`, receipt.CommandID); err != nil {
		t.Fatal(err)
	}
	task, err := s.claimExecutionTask(ctx, service.ID, service.Credential, "monitor-registration-evidence-session", 0)
	if task != nil && task.ID == receipt.CommandID {
		t.Fatalf("unapproved read bypassed target fence: %v", err)
	}
}
