package center

import (
	"bytes"
	"context"
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
	heartbeat.Capabilities = testRuntimeCapabilities(heartbeat.Capabilities)
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
	if _, err := s.db.Exec(`UPDATE deployments SET config_json='{}',manifest_json=json_set(manifest_json,'$.images[0].name','xray-core','$.runtime.docker.containers[0].image','xray-core') WHERE application_id='retained-meridian'`); err != nil {
		t.Fatal(err)
	}
	authorizeReinstallTestPackage(t, s, "retained-meridian-deployment")
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

// Seed an old on-disk record, not a new executable package. Upgrade must keep
// this evidence, including incomplete attempts, without issuing it again.
func seedArchivedReinstallPreparation(t *testing.T, s *Store, node AgentCredential, input AgentReinstallApplicationInput, state string) string {
	t.Helper()
	id := "reinstall-package-archived"
	_, err := s.db.Exec(`INSERT INTO deployments(id,agent_id,app_key,app_version,manifest_json,config_json,operation,state,created_at,updated_at,application_id,service_address,runtime_generation)
 SELECT ?,agent_id,app_key,app_version,manifest_json,config_json,'install',?,created_at,updated_at,application_id,service_address,runtime_generation FROM deployments WHERE id='retained-meridian-deployment'`, id, state)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`INSERT INTO agent_reinstall_app_preparations(deployment_id,source_deployment_id,operation_id,application_id,plan_revision,source_revision,replacement_key,approval_json,task_json)
 VALUES(?,'retained-meridian-deployment',?,?,?,'frozen-source',X'0102','{}','{}')`, id, input.OperationID, input.ApplicationID, input.PlanRevision)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRetiredAgentReinstallPreparationKeepsBusinessFenced(t *testing.T) {
	s, node, input := reinstallPreparationFixture(t)
	ctx := context.Background()
	for range 2 {
		if _, err := s.QueueAgentReinstallPreparation(ctx, node.ID, "reinstall-review-admin", input); err == nil {
			t.Fatal("retired proxy preparation was queued")
		}
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Applications) != 1 || plan.Applications[0].Recovery != "restore_data" || plan.Applications[0].Preparation != nil {
		t.Fatalf("proxy recovery still platform-owned: %+v", plan.Applications)
	}
	var deployments, preparations, activated int
	if err := s.db.QueryRow(`SELECT count(*) FROM deployments WHERE application_id=?`, input.ApplicationID).Scan(&deployments); err != nil || deployments != 1 {
		t.Fatalf("original installation replaced: %d %v", deployments, err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM agent_reinstall_app_preparations`).Scan(&preparations); err != nil || preparations != 0 {
		t.Fatalf("retired preparation persisted: %d %v", preparations, err)
	}
	if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM agent_network_profiles WHERE agent_id=?)+(SELECT count(*) FROM services WHERE application_id=?)`, node.ID, input.ApplicationID).Scan(&activated); err != nil || activated != 0 {
		t.Fatalf("business activated: %d %v", activated, err)
	}
	if _, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0); !errors.Is(err, errExecutionBlocked) {
		t.Fatalf("recovery fence released: %v", err)
	}
	directory := s.dataDir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.QueueAgentReinstallPreparation(ctx, node.ID, "reinstall-review-admin", input); err == nil {
		t.Fatal("restart revived retired preparation")
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
		for _, mode := range []string{"key", "source", "payload", "approval", "administrator", "receipt", "package-digest", "package-revision", "package-permissions"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				s, node, input := reinstallPreparationFixture(t)
				ctx := context.Background()
				id := seedArchivedReinstallPreparation(t, s, node, input, "pending")
				receipt := AgentReinstallPreparation{DeploymentID: id}
				var err error
				if stage == "projection" {
					if _, err = s.db.Exec(`UPDATE deployments SET state='running',attempt=1 WHERE id=?`, id); err != nil {
						t.Fatal(err)
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
				case "package-digest":
					query = `UPDATE deployments SET manifest_sha256=? WHERE id=?`
					args = []any{strings.Repeat("f", 64), receipt.DeploymentID}
				case "package-revision":
					query = `UPDATE deployments SET package_revision=999 WHERE id=?`
					args = []any{receipt.DeploymentID}
				case "package-permissions":
					query = `UPDATE deployments SET authorized_capabilities_json='["devices"]' WHERE id=?`
					args = []any{receipt.DeploymentID}
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
					tx, beginErr := s.db.BeginTx(ctx, nil)
					if beginErr != nil {
						t.Fatal(beginErr)
					}
					_, _, projectionErr := s.projectApplicationDeployment(ctx, tx, node.ID, id, 1, true, "", json.RawMessage(`{"services":[]}`), false, platform.ApplicationRuntimeGeneration)
					_ = tx.Rollback()
					if projectionErr == nil {
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
	id := seedArchivedReinstallPreparation(t, s, node, input, "failed")
	if _, err := s.QueueAgentReinstallPreparation(context.Background(), node.ID, "reinstall-review-admin", input); err == nil {
		t.Fatal("retired failed preparation was resumed")
	}
	if _, err := s.claimExecutionTask(context.Background(), node.ID, node.Credential, "package-preparation-session", 0); !errors.Is(err, errExecutionBlocked) {
		t.Fatalf("failed task released fence: %v", err)
	}
	var state string
	if err := s.db.QueryRow(`SELECT state FROM deployments WHERE id=?`, id).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("historical failure rewritten: %s %v", state, err)
	}
}

func TestAgentReinstallPreparationAuthorizationRejectsSubstitutedPayload(t *testing.T) {
	s, node, input := reinstallPreparationFixture(t)
	id := seedArchivedReinstallPreparation(t, s, node, input, "pending")
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	task := AgentTask{ID: id, Kind: "application.apply", ApplicationID: input.ApplicationID, AppKey: meridianAppKey, ServiceAddress: "10.0.0.99", Attempt: 1}
	if _, err := s.persistExecutionAuthorization(ctx, tx, node.ID, "package-preparation-session", task); err == nil {
		t.Fatal("retired substituted payload sealed")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var state string
	var attempt int
	if err := s.db.QueryRow(`SELECT state,attempt FROM deployments WHERE id=?`, id).Scan(&state, &attempt); err != nil || state != "pending" || attempt != 0 {
		t.Fatalf("rejected authorization changed history: %s/%d %v", state, attempt, err)
	}
}

func TestAgentReinstallPreparationExpiredClaimRequiresReview(t *testing.T) {
	s, node, input := reinstallPreparationFixture(t)
	id := seedArchivedReinstallPreparation(t, s, node, input, "running")
	if _, err := s.db.Exec(`UPDATE deployments SET attempt=1,lease_expires_at='2000-01-01T00:00:00Z' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.claimExecutionTask(context.Background(), node.ID, node.Credential, "package-preparation-session", 0); !errors.Is(err, errExecutionBlocked) {
		t.Fatalf("expired retired work replayed: %v", err)
	}
	var state string
	var attempt int
	if err := s.db.QueryRow(`SELECT state,attempt FROM deployments WHERE id=?`, id).Scan(&state, &attempt); err != nil || state != "running" || attempt != 1 {
		t.Fatalf("uncertain evidence rewritten: %s/%d %v", state, attempt, err)
	}
}
