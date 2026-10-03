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
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/networking"
)

const restoreNativeID = "11111111-1111-4111-8111-111111111111"
const restoreRouteID = "22222222-2222-4222-8222-222222222222"

func reinstallRuntimeFixture(t *testing.T) (*Store, AgentCredential, AgentReinstallApplicationInput) {
	return reinstallRuntimeNetworkFixture(t, false)
}

func reinstallRuntimeNetworkFixture(t *testing.T, public bool, publicExit ...string) (*Store, AgentCredential, AgentReinstallApplicationInput) {
	t.Helper()
	s, node, input := reinstallPreparationNetworkFixture(t, public, publicExit...)
	ctx := context.Background()
	if _, err := s.QueueAgentReinstallPreparation(ctx, node.ID, "reinstall-review-admin", input); err != nil {
		t.Fatal(err)
	}
	task, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
	if err != nil || task == nil {
		t.Fatal(err)
	}
	if response := preparationResult(t, s, node, task, true); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.Exec(`INSERT INTO services(id,application_id,site_id,name,display_name,protocol,container_port,host_port,endpoint,source,app_protocol,management,observed_listen,status,created_at,updated_at)
 SELECT 'restore-service',id,site_id,'entry','Entry','tcp',10443,10443,'10.0.0.7:10443','observed',?,0,'10.0.0.7','ready',?,? FROM applications WHERE id=?`, meridianEntryProtocol, now, now, input.ApplicationID); err != nil {
		t.Fatal(err)
	}
	endpointSecret, err := s.putSecret(ctx, tx, []byte("original-private-key"), meridianEndpointSecretContext("restore-endpoint"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO meridian_endpoints(id,application_id,service_id,inbound_tag,listen_address,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,fingerprint,desired_revision,applied_revision,runtime_healthy,status,created_at,updated_at)
 VALUES('restore-endpoint',?,'restore-service','restore-entry','10.0.0.7',10443,'entry.example.test',443,'www.example.com:443','203.0.113.20','["www.example.com"]',?,'original-public-key','["abcd"]','chrome',4,4,1,'ready',?,?)`, input.ApplicationID, endpointSecret, now, now); err != nil {
		t.Fatal(err)
	}
	token, err := s.putSecret(ctx, tx, []byte("retained-subscription-token"), meridianAccountSecretContext("restore-account"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO meridian_accounts(id,display_name,total_bytes,enabled,subscription_token_secret_id,subscription_token_sha256,desired_revision,applied_revision,status,created_at,updated_at)
 VALUES('restore-account','Account',0,1,?,?,1,1,'active',?,?)`, token, meridian.SubscriptionTokenFingerprint("retained-subscription-token"), now, now); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		id, kind, protocol string
		egress             any
	}{{"native", "native", restoreNativeID, nil}, {"route", "route", restoreRouteID, node.ID}} {
		key, err := s.putSecret(ctx, tx, []byte(entry.protocol), meridianCredentialSecretContext(entry.id))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`INSERT INTO meridian_credentials(id,account_id,endpoint_id,kind,user_name,identity_sha256,protocol_secret_id,egress_node_id,enabled,created_at,updated_at)
 VALUES(?,'restore-account','restore-endpoint',?,?,?,?,?,1,?,?)`, entry.id, entry.kind, entry.id, meridian.Identity(entry.protocol), key, entry.egress, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`INSERT INTO meridian_usage_watermarks(credential_id,observed_bytes,observed_at) VALUES(?,0,?)`, entry.id, now); err != nil {
			t.Fatal(err)
		}

	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.PlanRevision = plan.Revision
	return s, node, input
}

func submitRestoredRuntime(t *testing.T, s *Store, node AgentCredential, task *AgentTask, valid, succeeded bool) *httptest.ResponseRecorder {
	t.Helper()
	result := meridianHealthResult(meridianRuntimeProjection{task: *task.MeridianRuntime}, s.now().UTC(), true)
	// Fresh machines have no superseded 3x-ui installation to retire.
	result.LegacyRetired = true
	if !valid {
		result.Receipt.ConfigSHA256 = strings.Repeat("f", 64)
	}
	return submitReinstallRuntimeResult(t, s, node, task, result, succeeded)
}

func submitReinstallRuntimeResult(t *testing.T, s *Store, node AgentCredential, task *AgentTask, result meridianruntime.Result, succeeded bool) *httptest.ResponseRecorder {
	t.Helper()
	if err := s.StartExecution(context.Background(), node.ID, "package-preparation-session", task.Authorization.ID, task.Authorization.Digest); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"executionId": task.Authorization.ID, "sessionId": "package-preparation-session", "attempt": task.Attempt, "succeeded": succeeded, "result": ApplicationTaskResult{MeridianRuntime: &result}})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+node.ID+"/tasks/"+task.ID+"/result", bytes.NewReader(input))
	request.Header.Set("Authorization", "Bearer "+node.Credential)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewServer(s, "", false).Handler().ServeHTTP(response, request)
	return response
}

func TestAgentReinstallRuntimeRestoresNativeIdentityWithoutPublishing(t *testing.T) {
	s, node, input := reinstallRuntimeFixture(t)
	ctx := context.Background()
	receipt, err := s.QueueAgentReinstallRuntime(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.QueueAgentReinstallRuntime(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || replay != receipt {
		t.Fatalf("repeat queue: %+v %v", replay, err)
	}
	task, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
	if err != nil || task == nil || task.MeridianRuntime == nil {
		t.Fatalf("claim: %+v %v", task, err)
	}
	config := string(task.MeridianRuntime.Desired.Config)
	if task.ID != receipt.CommandID || task.Revision != 5 || !strings.Contains(config, "10.0.0.8") || strings.Contains(config, "10.0.0.7") || !strings.Contains(config, restoreNativeID) || strings.Contains(config, restoreRouteID) || !strings.Contains(config, "original-private-key") || task.MeridianRuntime.Source != nil || len(task.MeridianRuntime.Peers) != 0 || task.MeridianRuntime.ReplacePendingState || task.MeridianRuntime.RetireLegacy {
		t.Fatal("restoration lost identity/address or enabled an unreviewed landing")
	}
	raw, _ := json.Marshal(task)
	var metadata string
	if err = s.db.QueryRow(`SELECT task_json FROM agent_reinstall_app_preparations`).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(metadata, restoreNativeID) || strings.Contains(metadata, "original-private-key") || len(raw) == 0 {
		t.Fatal("recovery metadata contains runtime secrets")
	}
	response := submitRestoredRuntime(t, s, node, task, true, true)
	if response.Code != http.StatusOK {
		t.Fatalf("result: %d %s", response.Code, response.Body.String())
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Applications[0].Preparation.Runtime.State != "succeeded" || plan.Recovery.State != "review_required" {
		t.Fatalf("receipt/fence: %+v", plan)
	}
	public, _ := json.Marshal(plan)
	if bytes.Contains(public, []byte(restoreNativeID)) || bytes.Contains(public, []byte("original-private-key")) {
		t.Fatal("recovery UI contains credentials")
	}
	var active int
	if err = s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM agent_network_profiles WHERE agent_id=?)+(SELECT COUNT(*) FROM meridian_endpoints WHERE id='restore-endpoint' AND runtime_healthy=1)`, node.ID).Scan(&active); err != nil || active != 0 {
		t.Fatal("runtime receipt activated network/business")
	}
	var address string
	if err = s.db.QueryRow(`SELECT endpoint FROM services WHERE id='restore-service'`).Scan(&address); err != nil || address != "10.0.0.7:10443" {
		t.Fatal("runtime receipt published replacement address")
	}
	if _, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0); !errors.Is(err, errExecutionBlocked) {
		t.Fatalf("runtime released work fence: %v", err)
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
	replay, err = s.QueueAgentReinstallRuntime(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || replay.CommandID != receipt.CommandID || replay.State != "succeeded" {
		t.Fatalf("restart lost receipt: %+v %v", replay, err)
	}
}

func TestAgentReinstallRuntimeRejectsChangedAuthorization(t *testing.T) {
	for _, stage := range []string{"claim", "result"} {
		for _, mode := range []string{"key", "configuration", "revision", "input", "digest", "receipt", "image"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				s, node, input := reinstallRuntimeFixture(t)
				ctx := context.Background()
				receipt, err := s.QueueAgentReinstallRuntime(ctx, node.ID, "reinstall-review-admin", input)
				if err != nil {
					t.Fatal(err)
				}
				var task *AgentTask
				if stage == "result" {
					task, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
					if err != nil || task == nil {
						t.Fatal(err)
					}
				}
				var query string
				var args []any
				switch mode {
				case "key":
					query = `UPDATE agents SET x25519_public_key=? WHERE id=?`
					args = []any{testAgentPublicKey(t), node.ID}
				case "configuration":
					query = `UPDATE meridian_credentials SET enabled=0 WHERE id='native'`
				case "revision":
					query = `UPDATE meridian_endpoints SET desired_revision=desired_revision+1 WHERE id='restore-endpoint'`
				case "input":
					query = `UPDATE application_commands SET input_json=json_set(input_json,'$.replacePendingState',json('true')) WHERE id=?`
					args = []any{receipt.CommandID}
				case "digest":
					query = `UPDATE agent_reinstall_app_preparations SET runtime_task_sha256=''`
				case "receipt":
					query = `UPDATE agent_reinstall_app_preparations SET runtime_command_id=NULL`
				case "image":
					query = `UPDATE deployments SET manifest_json=json_set(manifest_json,'$.images[0].reference','example.test/changed:tag') WHERE id='retained-meridian-deployment'`
				}
				if _, err = s.db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
				if stage == "claim" {
					if _, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0); err == nil {
						t.Fatal("changed authority claimed")
					}
				} else {
					if response := submitRestoredRuntime(t, s, node, task, true, true); response.Code == http.StatusOK {
						t.Fatal("changed authority projected")
					}
				}
			})
		}
	}
}

func TestAgentReinstallRuntimeRequiresApprovalAndPreparedImage(t *testing.T) {
	for _, mode := range []string{"administrator", "plan", "image", "work", "endpoint"} {
		t.Run(mode, func(t *testing.T) {
			s, node, input := reinstallRuntimeFixture(t)
			admin := "reinstall-review-admin"
			switch mode {
			case "administrator":
				admin = "different-admin"
			case "plan":
				input.PlanRevision = strings.Repeat("f", 64)
			case "image":
				if _, err := s.db.Exec(`UPDATE deployments SET state='failed' WHERE id LIKE 'reinstall-package-%'`); err != nil {
					t.Fatal(err)
				}
			case "endpoint":
				if _, err := s.db.Exec(`UPDATE meridian_endpoints SET target='' WHERE id='restore-endpoint'`); err != nil {
					t.Fatal(err)
				}
				plan, err := s.AgentReinstallPlan(context.Background(), node.ID)
				if err != nil {
					t.Fatal(err)
				}
				input.PlanRevision = plan.Revision
			case "work":
				if _, err := s.db.Exec(`UPDATE task_executions SET state='unknown' WHERE agent_id=?`, node.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.QueueAgentReinstallRuntime(context.Background(), node.ID, admin, input); err == nil {
				t.Fatal("unreviewed runtime queued")
			}
			var revision int
			if err := s.db.QueryRow(`SELECT desired_revision FROM meridian_endpoints WHERE id='restore-endpoint'`).Scan(&revision); err != nil || revision != 4 {
				t.Fatal("rejected queue changed endpoint")
			}
		})
	}
}

func TestAgentReinstallRuntimeRejectsChangedReviewedIntent(t *testing.T) {
	for _, query := range []string{
		`UPDATE meridian_credentials SET enabled=0 WHERE kind='native'`,
		`UPDATE meridian_endpoints SET advertise_host='replacement.example'`,
		`UPDATE meridian_accounts SET enabled=0`,
		`UPDATE secrets SET sealed=x'00' WHERE id=(SELECT private_key_secret_id FROM meridian_endpoints LIMIT 1)`,
	} {
		t.Run(query, func(t *testing.T) {
			s, node, input := reinstallRuntimeFixture(t)
			if _, err := s.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			_, err := s.QueueAgentReinstallRuntime(context.Background(), node.ID, "reinstall-review-admin", input)
			if err == nil || !strings.Contains(err.Error(), "recovery plan changed") {
				t.Fatalf("stale review accepted: %v", err)
			}
			var revision int
			if err := s.db.QueryRow(`SELECT desired_revision FROM meridian_endpoints WHERE id='restore-endpoint'`).Scan(&revision); err != nil || revision != 4 {
				t.Fatal("stale review changed endpoint")
			}
		})
	}
}

func TestAgentReinstallRuntimeReviewIgnoresObservations(t *testing.T) {
	s, node, input := reinstallRuntimeFixture(t)
	if _, err := s.db.Exec(`UPDATE meridian_endpoints SET runtime_healthy=0,status='failed',last_error='old observation',updated_at='later'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueueAgentReinstallRuntime(context.Background(), node.ID, "reinstall-review-admin", input); err != nil {
		t.Fatalf("observation invalidated desired-state review: %v", err)
	}
}

func TestAgentReinstallRuntimeRejectsInvalidAndFailedResults(t *testing.T) {
	for _, mode := range []string{"invalid", "failed"} {
		t.Run(mode, func(t *testing.T) {
			s, node, input := reinstallRuntimeFixture(t)
			ctx := context.Background()
			if _, err := s.QueueAgentReinstallRuntime(ctx, node.ID, "reinstall-review-admin", input); err != nil {
				t.Fatal(err)
			}
			task, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
			if err != nil || task == nil {
				t.Fatal(err)
			}
			response := submitRestoredRuntime(t, s, node, task, mode != "invalid", mode != "failed")
			if mode == "invalid" && response.Code == http.StatusOK || mode == "failed" && response.Code != http.StatusOK {
				t.Fatalf("unexpected response %d", response.Code)
			}
			plan, err := s.AgentReinstallPlan(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			state := plan.Applications[0].Preparation.Runtime.State
			if state != "failed" && state != "needs_review" {
				t.Fatalf("bad receipt appeared usable: %s", state)
			}
			if _, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0); !errors.Is(err, errExecutionBlocked) {
				t.Fatalf("failed runtime replayed: %v", err)
			}
		})
	}
}

func TestAgentReinstallRuntimeSealingRejectsTaskSubstitution(t *testing.T) {
	s, node, input := reinstallRuntimeFixture(t)
	ctx := context.Background()
	if _, err := s.QueueAgentReinstallRuntime(ctx, node.ID, "reinstall-review-admin", input); err != nil {
		t.Fatal(err)
	}
	_, err := s.claimNextTask(ctx, node.ID, node.Credential, "", func(tx *sql.Tx, task *AgentTask) error {
		task.MeridianRuntime.ReplacePendingState = true
		_, err := s.persistExecutionAuthorization(ctx, tx, node.ID, "package-preparation-session", *task)
		return err
	})
	if !errors.Is(err, errExecutionAuthorization) {
		t.Fatalf("substitution accepted: %v", err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM application_commands WHERE id LIKE 'reinstall-runtime-%' AND state='pending' AND attempt=0`).Scan(&count); err != nil || count != 1 {
		t.Fatal("failed sealing committed a claim")
	}
}

func TestAgentReinstallRuntimeStartupHeartbeatRefreshesNetworkBeforeApproval(t *testing.T) {
	s, node, input := reinstallRuntimeNetworkFixture(t, true)
	ctx := context.Background()
	if _, err := s.QueueAgentReinstallRuntime(ctx, node.ID, "reinstall-review-admin", input); err != nil {
		t.Fatal(err)
	}
	task, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
	if err != nil || task == nil {
		t.Fatal(err)
	}
	result := meridianHealthResult(meridianRuntimeProjection{task: *task.MeridianRuntime}, s.now().UTC(), true)
	if response := submitReinstallRuntimeResult(t, s, node, task, result, true); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	var key []byte
	if err = s.db.QueryRow(`SELECT x25519_public_key FROM agents WHERE id=?`, node.ID).Scan(&key); err != nil {
		t.Fatal(err)
	}
	heartbeat := NodeHeartbeat{Startup: true, Version: Version, PublicKey: key, Capabilities: NodeCapabilities{Docker: true}, Roles: []string{"worker"}, NetworkCandidates: []networking.Candidate{{Address: "10.0.0.8", Interface: "eth0", Kind: "lan"}}, MeridianRuntime: &result}
	if err = s.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatalf("startup deadlocked before network refresh: %v", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.currentReinstallRuntimeObservation(ctx, tx, task.ID, *task.MeridianRuntime)
	tx.Rollback()
	if err == nil {
		t.Fatal("startup retained stale acceptance evidence")
	}
	heartbeat.Startup = false
	heartbeat.PublicEgress = &networking.PublicEgress{Address: "198.51.100.8", BindAddress: "10.0.0.8", Mode: networking.PublicModeNAT, ObservedAt: s.now().UTC()}
	if err = s.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatalf("fresh observation rejected: %v", err)
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.currentReinstallRuntimeObservation(ctx, tx, task.ID, *task.MeridianRuntime)
	tx.Rollback()
	if err != nil {
		t.Fatalf("fresh approved runtime unavailable: %v", err)
	}
}

func TestAgentReinstallRuntimeReviewedSuccessorPreservesReceipt(t *testing.T) {
	s, node, input := reinstallRuntimeFixture(t)
	ctx := context.Background()
	first, err := s.QueueAgentReinstallRuntime(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
	if err != nil || task == nil {
		t.Fatal(err)
	}
	if response := submitRestoredRuntime(t, s, node, task, true, true); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	var originalReceipt []byte
	if err = s.db.QueryRow(`SELECT sealed_result FROM task_executions WHERE task_id=?`, first.CommandID).Scan(&originalReceipt); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE meridian_endpoints SET desired_revision=desired_revision+1 WHERE id='restore-endpoint'`); err != nil {
		t.Fatal(err)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil || plan.Applications[0].Preparation.Runtime.State != "review_changed" {
		t.Fatalf("changed review: %v", err)
	}
	if _, err = s.QueueAgentReinstallRuntime(ctx, node.ID, "reinstall-review-admin", input); err == nil {
		t.Fatal("accepted stale review")
	}
	input.PlanRevision = plan.Revision
	next, err := s.QueueAgentReinstallRuntime(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || next.CommandID == first.CommandID {
		t.Fatalf("successor: %v", err)
	}
	repeat, err := s.QueueAgentReinstallRuntime(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || repeat != next {
		t.Fatal("duplicate review replayed task", err)
	}
	var retainedReceipt []byte
	var oldState string
	if err = s.db.QueryRow(`SELECT sealed_result FROM task_executions WHERE task_id=?`, first.CommandID).Scan(&retainedReceipt); err != nil || !bytes.Equal(originalReceipt, retainedReceipt) {
		t.Fatal("changed old receipt", err)
	}
	if err = s.db.QueryRow(`SELECT state FROM application_commands WHERE id=?`, first.CommandID).Scan(&oldState); err != nil || oldState != "succeeded" {
		t.Fatal("changed old command", err)
	}
	task, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
	if err != nil || task == nil || task.ID != next.CommandID {
		t.Fatal("claim successor", err)
	}
	if response := submitRestoredRuntime(t, s, node, task, true, true); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	plan, err = s.AgentReinstallPlan(ctx, node.ID)
	if err != nil || plan.Recovery.State != "review_required" || plan.Applications[0].Preparation.Runtime.State != "succeeded" {
		t.Fatal("successor bypassed final acceptance", err)
	}
}
