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

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
)

func reinstallPreparationFixture(t *testing.T) (*Store, AgentCredential, AgentReinstallApplicationInput) {
	return reinstallPreparationNetworkFixture(t, false)
}

func reinstallPreparationNetworkFixture(t *testing.T, public bool, publicExit ...string) (*Store, AgentCredential, AgentReinstallApplicationInput) {
	t.Helper()
	s, node, heartbeat := replacementNetworkFixture(t)
	exit := "198.51.100.8"
	if len(publicExit) > 0 {
		exit = publicExit[0]
	}
	ctx := context.Background()
	heartbeat.ApplicationRuntimeGeneration = platform.ApplicationRuntimeGeneration
	if public {
		if _, err := s.db.Exec(`UPDATE agent_network_profile_recovery SET profile_json=json_set(profile_json,'$.publicAddress','198.51.100.7','$.publicBindAddress','10.0.0.7','$.publicMode','nat','$.directPublic',json('true')) WHERE agent_id=?`, node.ID); err != nil {
			t.Fatal(err)
		}
		heartbeat.PublicEgress = &networking.PublicEgress{Address: exit, BindAddress: "10.0.0.8", Mode: networking.PublicModeNAT, ObservedAt: s.now().UTC()}
	}
	if err := s.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	addReinstallApplication(t, s, node, "retained-meridian", meridianAppKey, "0.1.0-alpha.12", "install", "succeeded")
	if _, err := s.db.Exec(`UPDATE deployments SET config_json='{}',manifest_json=json_set(manifest_json,'$.images[0].name','xray-core') WHERE application_id='retained-meridian'`); err != nil {
		t.Fatal(err)
	}
	networkInput := networkApprovalInput(t, s, node.ID)
	if public {
		networkInput.Profile.DirectPublic = true
		networkInput.Profile.PublicAddress = exit
		networkInput.Profile.PublicBindAddress = "10.0.0.8"
		networkInput.Profile.PublicMode = networking.PublicModeNAT
		networkInput.Profile.EnabledKinds = append(networkInput.Profile.EnabledKinds, networking.KindPublic)
	}
	if _, err := s.ApproveAgentReinstallNetwork(ctx, node.ID, "reinstall-review-admin", networkInput); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterExecutionSession(ctx, node.ID, node.Credential, "package-preparation-session", controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, node, AgentReinstallApplicationInput{OperationID: plan.Recovery.ID, PlanRevision: plan.Revision, ApplicationID: "retained-meridian"}
}

func preparationResult(t *testing.T, s *Store, node AgentCredential, task *AgentTask, succeeded bool) *httptest.ResponseRecorder {
	t.Helper()
	if err := s.StartExecution(context.Background(), node.ID, "package-preparation-session", task.Authorization.ID, task.Authorization.Digest); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"executionId": task.Authorization.ID, "sessionId": "package-preparation-session", "attempt": task.Attempt, "succeeded": succeeded, "applicationRuntimeGeneration": platform.ApplicationRuntimeGeneration, "result": json.RawMessage(`{"services":[]}`)})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+node.ID+"/tasks/"+task.ID+"/result", bytes.NewReader(input))
	request.Header.Set("Authorization", "Bearer "+node.Credential)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewServer(s, "", false).Handler().ServeHTTP(response, request)
	return response
}

func TestAgentReinstallPreparationRoundTripKeepsBusinessFenced(t *testing.T) {
	s, node, input := reinstallPreparationFixture(t)
	ctx := context.Background()
	before, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := s.QueueAgentReinstallPreparation(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.QueueAgentReinstallPreparation(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || replay != receipt {
		t.Fatalf("duplicate preparation: %+v %v", replay, err)
	}
	pendingPlan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil || len(pendingPlan.UnclaimedLocalWork) != 0 {
		t.Fatalf("new package treated as old work: %+v %v", pendingPlan.UnclaimedLocalWork, err)
	}
	task, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
	if err != nil || task == nil {
		t.Fatalf("claim: %+v %v", task, err)
	}
	if task.ID != receipt.DeploymentID || task.ID == "retained-meridian-deployment" || task.ApplicationID != input.ApplicationID || task.ServiceAddress != "10.0.0.8" || task.Manifest.Version != "0.1.0-alpha.12" || task.Attempt != 1 {
		t.Fatalf("wrong restore binding: %+v", task)
	}
	claimedPlan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pendingPlan.Recovery.UpdatedAt == before.Recovery.UpdatedAt || claimedPlan.Recovery.UpdatedAt == pendingPlan.Recovery.UpdatedAt {
		t.Fatal("queued/claimed progress did not refresh recovery")
	}
	response := preparationResult(t, s, node, task, true)
	if response.Code != http.StatusOK {
		t.Fatalf("result: %d %s", response.Code, response.Body.String())
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Applications) != 1 || plan.Applications[0].DeploymentID != "retained-meridian-deployment" || plan.Applications[0].Preparation == nil || plan.Applications[0].Preparation.State != "succeeded" || plan.Recovery.State != "review_required" {
		t.Fatalf("preparation became business restoration: %+v", plan)
	}
	if plan.Recovery.UpdatedAt == claimedPlan.Recovery.UpdatedAt {
		t.Fatal("completion did not refresh recovery")
	}
	var activated int
	if err = s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM agent_network_profiles WHERE agent_id=?)+(SELECT COUNT(*) FROM services WHERE application_id=?)`, node.ID, input.ApplicationID).Scan(&activated); err != nil || activated != 0 {
		t.Fatalf("activated business: %d %v", activated, err)
	}
	if _, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0); !errors.Is(err, errExecutionBlocked) {
		t.Fatalf("released fence: %v", err)
	}
	directory := s.dataDir
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	replay, err = s.QueueAgentReinstallPreparation(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || replay.DeploymentID != receipt.DeploymentID || replay.State != "succeeded" {
		t.Fatalf("restart lost receipt: %+v %v", replay, err)
	}
}

func TestAgentReinstallPreparationRejectsUnreviewedQueue(t *testing.T) {
	for _, mode := range []string{"admin", "plan", "operation", "app", "network", "uninstall", "config", "artifact"} {
		t.Run(mode, func(t *testing.T) {
			s, node, input := reinstallPreparationFixture(t)
			admin := "reinstall-review-admin"
			switch mode {
			case "admin":
				admin = "unapproved-admin"
			case "plan":
				input.PlanRevision = strings.Repeat("f", 64)
			case "operation":
				input.OperationID = "unapproved-operation"
			case "app":
				input.ApplicationID = "unreviewed-app"
			case "network":
				_, _ = s.db.Exec(`DELETE FROM agent_reinstall_network_approvals`)
			case "uninstall":
				_, _ = s.db.Exec(`UPDATE deployments SET operation='uninstall'`)
			case "config":
				_, _ = s.db.Exec(`UPDATE deployments SET config_json='{"unexpected":true}'`)
			case "artifact":
				_, _ = s.db.Exec(`UPDATE deployments SET manifest_json=json_set(manifest_json,'$.images[0].name','unreviewed')`)
			}
			if _, err := s.QueueAgentReinstallPreparation(context.Background(), node.ID, admin, input); err == nil {
				t.Fatal("unreviewed preparation accepted")
			}
			var count int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM agent_reinstall_app_preparations`).Scan(&count); err != nil || count != 0 {
				t.Fatal("rejected queue persisted task")
			}
		})
	}
}

func TestAgentReinstallPreparationRechecksBeforeClaimAndProjection(t *testing.T) {
	for _, stage := range []string{"claim", "projection"} {
		for _, mode := range []string{"key", "source", "payload", "approval", "administrator", "receipt"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				s, node, input := reinstallPreparationFixture(t)
				ctx := context.Background()
				receipt, err := s.QueueAgentReinstallPreparation(ctx, node.ID, "reinstall-review-admin", input)
				if err != nil {
					t.Fatal(err)
				}
				var task *AgentTask
				if stage == "projection" {
					task, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
					if err != nil || task == nil {
						t.Fatalf("claim: %+v %v", task, err)
					}
				}
				var query string
				var args []any
				switch mode {
				case "key":
					query = `UPDATE agents SET x25519_public_key=? WHERE id=?`
					args = []any{testAgentPublicKey(t), node.ID}
				case "source":
					query = `UPDATE deployments SET operation='uninstall' WHERE id='retained-meridian-deployment'`
				case "payload":
					query = `UPDATE deployments SET service_address='10.0.0.99' WHERE id=?`
					args = []any{receipt.DeploymentID}
				case "approval":
					query = `UPDATE agent_reinstall_network_approvals SET approval_json=json_set(approval_json,'$.approvedAt','2026-01-01T00:00:00Z')`
				case "receipt":
					query = `DELETE FROM agent_reinstall_app_preparations`
				case "administrator":
					query = `DELETE FROM admins WHERE id='reinstall-review-admin'`
				}
				if _, err = s.db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
				if stage == "claim" {
					if _, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0); err == nil {
						t.Fatal("changed authority claimed")
					}
				} else {
					response := preparationResult(t, s, node, task, true)
					if response.Code == http.StatusOK {
						t.Fatal("changed authority projected")
					}
				}
				var state string
				if err = s.db.QueryRow(`SELECT state FROM deployments WHERE id=?`, receipt.DeploymentID).Scan(&state); err != nil || state == "succeeded" {
					t.Fatal("invalid projection completed")
				}
			})
		}
	}
}

func TestAgentReinstallPreparationFailedOutcomeNeverRetries(t *testing.T) {
	s, node, input := reinstallPreparationFixture(t)
	ctx := context.Background()
	receipt, err := s.QueueAgentReinstallPreparation(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}
	response := preparationResult(t, s, node, task, false)
	if response.Code != http.StatusOK {
		t.Fatalf("failure: %d %s", response.Code, response.Body.String())
	}
	replay, err := s.QueueAgentReinstallPreparation(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || replay.DeploymentID != receipt.DeploymentID || replay.State != "failed" {
		t.Fatalf("failure retried: %+v %v", replay, err)
	}
	if _, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0); !errors.Is(err, errExecutionBlocked) {
		t.Fatalf("failed task released fence: %v", err)
	}
}

func TestAgentReinstallPreparationAuthorizationRejectsSubstitutedPayload(t *testing.T) {
	s, node, input := reinstallPreparationFixture(t)
	ctx := context.Background()
	receipt, err := s.QueueAgentReinstallPreparation(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.claimNextTask(ctx, node.ID, node.Credential, "", func(tx *sql.Tx, task *AgentTask) error {
		task.ServiceAddress = "10.0.0.99"
		_, err := s.persistExecutionAuthorization(ctx, tx, node.ID, "package-preparation-session", *task)
		return err
	})
	if !errors.Is(err, errExecutionAuthorization) {
		t.Fatalf("substituted payload sealed: %v", err)
	}
	var state string
	var attempt int
	if err = s.db.QueryRow(`SELECT state,attempt FROM deployments WHERE id=?`, receipt.DeploymentID).Scan(&state, &attempt); err != nil || state != "pending" || attempt != 0 {
		t.Fatal("failed authorization committed selection")
	}
}

func TestAgentReinstallPreparationExpiredClaimRequiresReview(t *testing.T) {
	s, node, input := reinstallPreparationFixture(t)
	ctx := context.Background()
	receipt, err := s.QueueAgentReinstallPreparation(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err = s.db.Exec(`UPDATE deployments SET lease_expires_at='2000-01-01T00:00:00Z' WHERE id=?`, receipt.DeploymentID); err != nil {
		t.Fatal(err)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil || plan.Applications[0].Preparation.State != "needs_review" {
		t.Fatalf("uncertain task shown as in progress: %+v %v", plan.Applications, err)
	}
	if _, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0); !errors.Is(err, errExecutionBlocked) {
		t.Fatalf("expired work replayed: %v", err)
	}
}
