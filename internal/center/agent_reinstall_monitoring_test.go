package center

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/pulse"
	"github.com/petauron/vastora/internal/secret"
)

func reinstallMonitorFixture(t *testing.T) (*Store, AgentCredential, AgentCredential, DeploymentView, *AgentTask) {
	t.Helper()
	s, service, collector, serviceDeployment := pulseFixture(t)
	ctx := context.Background()
	addPulsePrivateFixture(t, s, serviceDeployment.ApplicationID, service.ID)
	install, err := s.CreateDeployment(ctx, DeploymentRequest{AgentID: collector.ID, AppKey: pulseAgentAppKey})
	if err != nil {
		t.Fatal(err)
	}
	const session = "monitor-registration-evidence-session"
	if err = s.RegisterExecutionSession(ctx, service.ID, service.Credential, session, controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	task, err := s.claimExecutionTask(ctx, service.ID, service.Credential, session, 0)
	if err != nil || task == nil || task.PulseEnrollment == nil {
		t.Fatalf("registration claim: %v", err)
	}
	if err = s.StartExecution(ctx, service.ID, session, task.Authorization.ID, task.Authorization.Digest); err != nil {
		t.Fatal(err)
	}
	result, _ := json.Marshal(map[string]any{"pulseEnrollment": pulse.EnrollmentResult{ID: "original-registration", Token: strings.Repeat("test-secret-", 3), ExpiresAtUnixMS: s.now().Add(time.Hour).UnixMilli()}})
	input, _ := json.Marshal(map[string]any{"executionId": task.Authorization.ID, "sessionId": session, "attempt": task.Attempt, "succeeded": true, "result": json.RawMessage(result)})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+service.ID+"/tasks/"+task.ID+"/result", bytes.NewReader(input))
	request.Header.Set("Authorization", "Bearer "+service.Credential)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewServer(s, "", false).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("registration result: %d %s", response.Code, response.Body.String())
	}
	completeNextTask(t, s, collector, "application.apply", json.RawMessage(`{"services":[]}`))
	return s, service, collector, install, task
}

func TestAgentReinstallMonitoringReadsOriginalRegistrationAfterConfigureAndExpiry(t *testing.T) {
	s, service, collector, installed, task := reinstallMonitorFixture(t)
	ctx := context.Background()
	if _, err := s.CreateDeployment(ctx, DeploymentRequest{AgentID: collector.ID, AppKey: pulseAgentAppKey, Operation: "configure", Config: json.RawMessage(`{"node_region":"US"}`)}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, s, collector, "application.apply", json.RawMessage(`{"services":[]}`))
	clock := s.now().Add(2 * time.Hour)
	s.now = func() time.Time { return clock }
	var before, after int64
	if err := s.db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	plan, err := s.AgentReinstallPlan(ctx, collector.ID)
	if err != nil || len(plan.Monitoring) != 1 {
		t.Fatalf("monitor inventory: %+v %v", plan.Monitoring, err)
	}
	review := plan.Monitoring[0]
	if review.State != "inspection_required" || review.ApplicationID != installed.ApplicationID || review.ServiceAgentID != service.ID || review.ServiceApplicationID != task.PulseEnrollment.ApplicationID || len(review.Enrollments) != 1 || review.Enrollments[0].EnrollmentID != "original-registration" || review.Enrollments[0].ExecutionID != task.Authorization.ID {
		t.Fatalf("wrong original registration association: %+v", review)
	}
	if err := s.db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil || before != after {
		t.Fatalf("review mutated stored state: %d %d %v", before, after, err)
	}
	encoded, _ := json.Marshal(plan)
	for _, value := range []string{"test-secret-", "setup_token", "enrollment_token", "sealed_result"} {
		if bytes.Contains(encoded, []byte(value)) {
			t.Fatalf("review exposed %q", value)
		}
	}
	// The original source identity can be retired without losing its historical
	// association. This evidence grants no authority to replay that execution.
	if _, err := s.db.Exec(`UPDATE task_executions SET identity_retired_at='retired' WHERE id=?`, task.Authorization.ID); err != nil {
		t.Fatal(err)
	}
	plan, err = s.AgentReinstallPlan(ctx, collector.ID)
	if err != nil || plan.Monitoring[0].State != "inspection_required" {
		t.Fatalf("retired execution erased historical registration: %v", err)
	}
	if _, err := s.CreateDeployment(ctx, DeploymentRequest{AgentID: collector.ID, AppKey: pulseAgentAppKey, Operation: "uninstall"}); err != nil {
		t.Fatal(err)
	}
	plan, err = s.AgentReinstallPlan(ctx, collector.ID)
	if err != nil || len(plan.Monitoring) != 0 {
		t.Fatalf("pending uninstall requested monitor restoration: %+v %v", plan.Monitoring, err)
	}
}

func TestAgentReinstallMonitoringRejectsUnverifiableAssociations(t *testing.T) {
	for _, mode := range []string{"no-execution", "no-result", "corrupt-result", "task-digest", "task-deployment", "task-service", "task-attempt", "failed-result", "unknown-result", "missing-enrollment", "service-owner", "configuration"} {
		t.Run(mode, func(t *testing.T) {
			s, _, collector, install, task := reinstallMonitorFixture(t)
			id := task.Authorization.ID
			var statement string
			var arguments []any
			switch mode {
			case "no-execution":
				statement, arguments = `DELETE FROM task_executions WHERE id=?`, []any{id}
			case "no-result":
				statement, arguments = `UPDATE task_executions SET sealed_result=X'' WHERE id=?`, []any{id}
			case "corrupt-result":
				statement, arguments = `UPDATE task_executions SET sealed_result=X'1234' WHERE id=?`, []any{id}
			case "task-digest":
				statement, arguments = `UPDATE task_executions SET digest='invalid' WHERE id=?`, []any{id}
			case "service-owner":
				statement, arguments = `UPDATE applications SET node_id=? WHERE id=?`, []any{collector.ID, task.PulseEnrollment.ApplicationID}
			case "configuration":
				statement, arguments = `UPDATE deployments SET config_json='{}' WHERE id=?`, []any{install.ID}
			default:
				column, contextName := "sealed_task", "execution-task:"
				if strings.HasSuffix(mode, "result") || mode == "missing-enrollment" {
					column, contextName = "sealed_result", "execution-result:"
				}
				var sealed []byte
				if err := s.db.QueryRow(`SELECT `+column+` FROM task_executions WHERE id=?`, id).Scan(&sealed); err != nil {
					t.Fatal(err)
				}
				raw, err := secret.Open(s.key, sealed, []byte(contextName+id))
				if err != nil {
					t.Fatal(err)
				}
				if column == "sealed_task" {
					var original AgentTask
					if err := json.Unmarshal(raw, &original); err != nil {
						t.Fatal(err)
					}
					switch mode {
					case "task-deployment":
						original.PulseEnrollment.DeploymentID = "different-collector-deployment"
					case "task-service":
						original.PulseEnrollment.ApplicationID = "different-monitor-service"
					case "task-attempt":
						original.Attempt++
					}
					raw, _ = json.Marshal(original)
				} else {
					var result executionResultEvidence
					if err := json.Unmarshal(raw, &result); err != nil {
						t.Fatal(err)
					}
					switch mode {
					case "failed-result":
						result.Succeeded = false
					case "unknown-result":
						result.Unknown = true
					case "missing-enrollment":
						result.Result = json.RawMessage(`{}`)
					}
					raw, _ = json.Marshal(result)
				}
				sealed, err = secret.Seal(s.key, raw, []byte(contextName+id))
				if err != nil {
					t.Fatal(err)
				}
				statement, arguments = `UPDATE task_executions SET `+column+`=? WHERE id=?`, []any{sealed, id}
				if column == "sealed_task" {
					digest := sha256.Sum256(raw)
					statement, arguments = `UPDATE task_executions SET sealed_task=?,digest=? WHERE id=?`, []any{sealed, hex.EncodeToString(digest[:]), id}
				}
			}
			if _, err := s.db.Exec(statement, arguments...); err != nil {
				t.Fatal(err)
			}
			plan, err := s.AgentReinstallPlan(context.Background(), collector.ID)
			if err != nil || len(plan.Monitoring) != 1 || plan.Monitoring[0].State == "inspection_required" || len(plan.Monitoring[0].Enrollments) != 0 {
				t.Fatalf("unverifiable registration accepted: %+v %v", plan.Monitoring, err)
			}
		})
	}
}

func TestAgentReinstallMonitoringReviewBindsRetainedEvidence(t *testing.T) {
	s, _, collector, _, task := reinstallMonitorFixture(t)
	ctx := context.Background()
	before, err := s.AgentReinstallPlan(ctx, collector.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Even metadata-equivalent evidence changes require another explicit review.
	var sealed []byte
	id := task.Authorization.ID
	if err = s.db.QueryRow(`SELECT sealed_result FROM task_executions WHERE id=?`, id).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	raw, err := secret.Open(s.key, sealed, []byte("execution-result:"+id))
	if err != nil {
		t.Fatal(err)
	}
	resealed, err := secret.Seal(s.key, raw, []byte("execution-result:"+id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE task_executions SET sealed_result=? WHERE id=?`, resealed, id); err != nil {
		t.Fatal(err)
	}
	after, err := s.AgentReinstallPlan(ctx, collector.ID)
	if err != nil || before.Revision == after.Revision || after.Monitoring[0].State != "inspection_required" {
		t.Fatalf("changed retained source did not invalidate review: %v", err)
	}
}

func TestAgentReinstallMonitoringDoesNotSelectPartialOrUnrelatedHistory(t *testing.T) {
	for _, mode := range []string{"partial", "limit", "unrelated", "missing"} {
		t.Run(mode, func(t *testing.T) {
			s, service, collector, _, task := reinstallMonitorFixture(t)
			ctx := context.Background()
			before, err := s.AgentReinstallPlan(ctx, collector.ID)
			if err != nil {
				t.Fatal(err)
			}
			count, target := 1, collector.ID
			if mode == "limit" {
				count = reinstallMonitorEvidenceLimit
			} else if mode == "unrelated" {
				target = service.ID
			}
			if mode == "missing" {
				if _, err := s.db.Exec(`DELETE FROM application_commands WHERE id=?`, task.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				for i := 0; i < count; i++ {
					if _, err := s.db.Exec(`INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at)
					 SELECT ?,application_id,agent_id,?,kind,input_json,'failed',created_at,updated_at FROM application_commands WHERE id=?`, fmt.Sprintf("additional-monitor-registration-%d", i), target, task.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			after, err := s.AgentReinstallPlan(ctx, collector.ID)
			if err != nil || len(after.Monitoring) != 1 {
				t.Fatalf("monitor inventory: %+v %v", after.Monitoring, err)
			}
			expected := map[string]string{"partial": "evidence_invalid", "limit": "evidence_limit", "unrelated": "inspection_required", "missing": "evidence_missing"}[mode]
			if after.Monitoring[0].State != expected {
				t.Fatalf("wrong review state: %+v", after.Monitoring[0])
			}
			if mode == "unrelated" {
				if before.Revision != after.Revision || len(after.Monitoring[0].Enrollments) != 1 {
					t.Fatal("unrelated registration changed the collector review")
				}
			} else if len(after.Monitoring[0].Enrollments) != 0 || before.Revision == after.Revision {
				t.Fatal("partial evidence retained a candidate or stale review")
			}
		})
	}
}
