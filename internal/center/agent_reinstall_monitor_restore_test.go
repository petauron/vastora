package center

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
)

func monitorRestoreFixture(t *testing.T) (*Store, AgentCredential, AgentCredential, AgentReinstallMonitorInput) {
	t.Helper()
	s, service, node, input := monitorRotationFixture(t)
	ctx := context.Background()
	if _, err := s.QueueAgentReinstallMonitorRotation(ctx, node.ID, "reinstall-review-admin", input); err != nil {
		t.Fatal(err)
	}
	task, err := s.claimExecutionTask(ctx, service.ID, service.Credential, "monitor-registration-evidence-session", 0)
	if err != nil || task == nil {
		t.Fatalf("rotation: %v", err)
	}
	if response := submitMonitorInspection(t, s, service, task, successfulMonitorRotationJSON(), true); response.Code != http.StatusOK {
		t.Fatalf("rotation: %s", response.Body.String())
	}
	var key []byte
	if err = s.db.QueryRow(`SELECT x25519_public_key FROM agents WHERE id=?`, node.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	heartbeat := NodeHeartbeat{Version: Version, PublicKey: key, Capabilities: NodeCapabilities{PulseRestore: true}, Roles: []string{"worker"}, ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration, NetworkCandidates: []networking.Candidate{{Address: "10.0.0.8", Interface: "eth0", Kind: "lan"}}}
	if err = s.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApproveAgentReinstallNetwork(ctx, node.ID, "reinstall-review-admin", networkApprovalInput(t, s, node.ID)); err != nil {
		t.Fatal(err)
	}
	if err = s.RegisterExecutionSession(ctx, node.ID, node.Credential, "monitor-original-restore-session", controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.PlanRevision = plan.Revision
	return s, service, node, input
}

func monitorRestoreResult(t *testing.T, s *Store, node AgentCredential, task *AgentTask, raw json.RawMessage) *httptest.ResponseRecorder {
	t.Helper()
	if err := s.StartExecution(context.Background(), node.ID, "monitor-original-restore-session", task.Authorization.ID, task.Authorization.Digest); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"executionId": task.Authorization.ID, "sessionId": "monitor-original-restore-session", "attempt": task.Attempt, "succeeded": true, "applicationRuntimeGeneration": platform.ApplicationRuntimeGeneration, "result": raw})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+node.ID+"/tasks/"+task.ID+"/result", bytes.NewReader(input))
	request.Header.Set("Authorization", "Bearer "+node.Credential)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewServer(s, "", false).Handler().ServeHTTP(response, request)
	return response
}

func TestAgentReinstallMonitorRestoreOriginalCredentialsRoundTrip(t *testing.T) {
	s, _, node, input := monitorRestoreFixture(t)
	ctx := context.Background()
	before, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := s.QueueAgentReinstallMonitorRestore(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.QueueAgentReinstallMonitorRestore(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || replay != receipt {
		t.Fatalf("repeat: %+v %v", replay, err)
	}
	pending, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.UnclaimedLocalWork) != 0 {
		t.Fatal("new recovery install can be cancelled as historical work")
	}
	task, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "monitor-original-restore-session", 0)
	if err != nil || task == nil || task.ID != receipt.DeploymentID || task.PulseRestore == nil {
		t.Fatalf("restore claim: %+v %v", task, err)
	}
	if task.PulseRestore.NodeID != "11111111-1111-4111-8111-111111111111" || task.PulseRestore.Token != "test-rotated-credential-never-publish" || string(task.Secrets) != "{}" || task.PulseEnrollment != nil || task.Operation != "install" {
		t.Fatal("wrong credential or enrollment replay")
	}
	if task.Manifest.Version != before.Applications[0].Version {
		t.Fatal("saved package version changed")
	}
	response := monitorRestoreResult(t, s, node, task, json.RawMessage(`{"services":[],"pulseRestored":{"nodeId":"11111111-1111-4111-8111-111111111111"}}`))
	if response.Code != http.StatusOK {
		t.Fatalf("restore result: %s", response.Body.String())
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved := plan.Monitoring[0].Restoration
	if saved == nil || saved.State != "succeeded" {
		t.Fatalf("restore receipt: %+v", saved)
	}
	if plan.Applications[0].DeploymentID != before.Applications[0].DeploymentID {
		t.Fatal("original intent overwritten by recovery work")
	}
	if _, err = s.ClaimNextTask(ctx, node.ID, node.Credential); !errors.Is(err, errExecutionBlocked) {
		t.Fatalf("collector install released recovery fence: %v", err)
	}
	raw, _ := json.Marshal(plan)
	if bytes.Contains(raw, []byte("test-rotated-credential")) {
		t.Fatal("credential leaked into plan")
	}
	var enrollments int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM application_commands WHERE kind='pulse.enrollment.create'`).Scan(&enrollments); err != nil || enrollments != 1 {
		t.Fatal("created another monitoring identity")
	}
	input.PlanRevision = plan.Revision
	if replay, err = s.QueueAgentReinstallMonitorRestore(ctx, node.ID, "reinstall-review-admin", input); err != nil || replay != *saved {
		t.Fatalf("terminal replay: %+v %v", replay, err)
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
	if err != nil || after.Monitoring[0].Restoration == nil || *after.Monitoring[0].Restoration != *saved {
		t.Fatalf("restart receipt: %v", err)
	}
}

func TestAgentReinstallMonitorRestoreRequiresAuthenticCurrentEvidence(t *testing.T) {
	for _, stage := range []string{"queue", "selection", "authorization", "projection"} {
		for _, mode := range []string{"key", "source", "rotation-result", "network", "capability"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				s, _, node, input := monitorRestoreFixture(t)
				ctx := context.Background()
				var receipt AgentReinstallMonitorRestore
				var task *AgentTask
				var err error
				if stage != "queue" {
					receipt, err = s.QueueAgentReinstallMonitorRestore(ctx, node.ID, "reinstall-review-admin", input)
					if err != nil {
						t.Fatal(err)
					}
				}
				if stage == "authorization" || stage == "projection" {
					task, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "monitor-original-restore-session", 0)
					if err != nil || task == nil {
						t.Fatalf("claim: %v", err)
					}
				}
				switch mode {
				case "key":
					_, err = s.db.Exec(`UPDATE agents SET x25519_public_key=? WHERE id=?`, testAgentPublicKey(t), node.ID)
				case "source":
					_, err = s.db.Exec(`UPDATE deployments SET app_version='changed' WHERE application_id=? AND id NOT LIKE 'reinstall-monitor-collector-%'`, input.ApplicationID)
				case "rotation-result":
					_, err = s.db.Exec(`UPDATE task_executions SET sealed_result=X'1234' WHERE task_id IN(SELECT command_id FROM agent_reinstall_monitor_rotations)`)
				case "network":
					_, err = s.db.Exec(`DELETE FROM agent_reinstall_network_approvals`)
				case "capability":
					_, err = s.db.Exec(`UPDATE agents SET capabilities_json=json_remove(capabilities_json,'$.pulseRestore') WHERE id=?`, node.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				switch stage {
				case "queue":
					plan, err := s.AgentReinstallPlan(ctx, node.ID)
					if err != nil {
						t.Fatal(err)
					}
					input.PlanRevision = plan.Revision
					if _, err = s.QueueAgentReinstallMonitorRestore(ctx, node.ID, "reinstall-review-admin", input); err == nil {
						t.Fatal("invalid restore queued")
					}
				case "selection":
					selected, _ := s.claimExecutionTask(ctx, node.ID, node.Credential, "monitor-original-restore-session", 0)
					if selected != nil && selected.ID == receipt.DeploymentID {
						t.Fatal("invalid restore selected")
					}
				case "authorization":
					tx, err := s.db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					_, err = s.persistExecutionAuthorization(ctx, tx, node.ID, "monitor-original-restore-session", *task)
					_ = tx.Rollback()
					if err == nil {
						t.Fatal("invalid restore sealed")
					}
				case "projection":
					tx, err := s.db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					_, _, err = s.projectApplicationDeployment(ctx, tx, node.ID, task.ID, task.Attempt, true, "", json.RawMessage(`{"pulseRestored":{"nodeId":"11111111-1111-4111-8111-111111111111"}}`), false, platform.ApplicationRuntimeGeneration)
					_ = tx.Rollback()
					if err == nil {
						t.Fatal("invalid restore projected")
					}
				}
			})
		}
	}
}

func TestAgentReinstallMonitorRestoreMissingReceiptRemainsFenced(t *testing.T) {
	for _, mode := range []string{"missing", "wrong-node", "extra-service", "lost-authority", "expired"} {
		t.Run(mode, func(t *testing.T) {
			s, _, node, input := monitorRestoreFixture(t)
			ctx := context.Background()
			receipt, err := s.QueueAgentReinstallMonitorRestore(ctx, node.ID, "reinstall-review-admin", input)
			if err != nil {
				t.Fatal(err)
			}
			task, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "monitor-original-restore-session", 0)
			if err != nil || task == nil {
				t.Fatalf("claim: %v", err)
			}
			raw := json.RawMessage(`{}`)
			switch mode {
			case "wrong-node":
				raw = json.RawMessage(`{"pulseRestored":{"nodeId":"other"}}`)
			case "extra-service":
				raw = json.RawMessage(`{"pulseRestored":{"nodeId":"11111111-1111-4111-8111-111111111111"},"services":[{"name":"unexpected"}]}`)
			case "lost-authority":
				_, err = s.db.Exec(`DELETE FROM agent_reinstall_monitor_restorations WHERE deployment_id=?`, task.ID)
			case "expired":
				_, err = s.db.Exec(`UPDATE deployments SET lease_expires_at='2020-01-01T00:00:00Z' WHERE id=?`, task.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode != "expired" {
				response := monitorRestoreResult(t, s, node, task, raw)
				if response.Code == http.StatusOK {
					t.Fatal("bad restore proof accepted")
				}
			}
			plan, err := s.AgentReinstallPlan(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "lost-authority" && (plan.Monitoring[0].Restoration == nil || plan.Monitoring[0].Restoration.State != "needs_review") {
				t.Fatalf("lost proof not displayed: %+v", plan.Monitoring[0].Restoration)
			}
			if next, _ := s.claimExecutionTask(ctx, node.ID, node.Credential, "monitor-original-restore-session", 0); next != nil && next.ID == receipt.DeploymentID {
				t.Fatal("uncertain restore replayed")
			}
		})
	}
}
