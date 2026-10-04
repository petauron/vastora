package center

import (
	"context"
	"encoding/json"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/meridianruntime"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAgentReinstallAcceptanceRejectsUnreviewedExitAndStaleAuthority(t *testing.T) {
	s, node, input := reinstallEntryCheckFixture(t)
	ctx := context.Background()
	if _, err := s.ActivateAgentReinstallAccess(ctx, node.ID, "reinstall-review-admin", input); err != nil {
		t.Fatal(err)
	}
	enrollment, err := s.CreateAgentEnrollment(ctx, AgentEnrollmentSpec{SiteID: testSiteID(t, s), Name: "acceptance-verifier", CenterURL: "https://center.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := s.EnrollAgent(ctx, enrollment.Token, "test", "linux", "amd64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err = s.db.Exec(`UPDATE agents SET capabilities_json='{"docker":true,"meridianAcceptance":true}',last_seen_at=? WHERE id=?`, now, verifier.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE agents SET public_egress_address='1.1.1.1',public_egress_bind_address='1.1.1.1',public_egress_mode='direct',public_egress_observed_at=? WHERE id=?`, now, node.ID); err != nil {
		t.Fatal(err)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.PlanRevision = plan.Revision
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// The observed exit changed after approval. Even a public address must
	// not replace the approved binding just to make an acceptance test pass.
	if _, err = s.reinstallAcceptanceTasks(ctx, tx, node.ID, verifier.ID, "reinstall-review-admin", input); err == nil {
		t.Fatal("unreviewed exit accepted")
	}
	if _, err = s.reinstallAcceptanceTasks(ctx, tx, node.ID, node.ID, "reinstall-review-admin", input); err == nil {
		t.Fatal("self verification accepted")
	}
	changed := input
	changed.PlanRevision = "changed"
	if _, err = s.reinstallAcceptanceTasks(ctx, tx, node.ID, verifier.ID, "reinstall-review-admin", changed); err == nil {
		t.Fatal("stale plan accepted")
	}
	if _, err = tx.Exec(`UPDATE agents SET public_egress_observed_at=? WHERE id=?`, s.now().Add(-11*time.Minute).UTC().Format(time.RFC3339Nano), node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.reinstallAcceptanceExit(ctx, tx, node.ID, false); err == nil {
		t.Fatal("stale exit accepted")
	}
}

func TestAgentReinstallAcceptanceQueuesAndProjectsAuthenticatedResult(t *testing.T) {
	s, node, input := reinstallEntryCheckFixture(t, "1.1.1.1")
	ctx := context.Background()
	if _, err := s.ActivateAgentReinstallAccess(ctx, node.ID, "reinstall-review-admin", input); err != nil {
		t.Fatal(err)
	}
	enrollment, err := s.CreateAgentEnrollment(ctx, AgentEnrollmentSpec{SiteID: testSiteID(t, s), Name: "acceptance-verifier", CenterURL: "https://center.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := s.EnrollAgent(ctx, enrollment.Token, "test", "linux", "amd64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE agents SET capabilities_json='{"docker":true,"meridianAcceptance":true}',last_seen_at=? WHERE id=?`, s.now().UTC().Format(time.RFC3339Nano), verifier.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.RegisterExecutionSession(ctx, verifier.ID, verifier.Credential, "monitor-registration-evidence-session", controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.PlanRevision = plan.Revision
	if _, err = s.verifyAgentReinstallEntry(ctx, node.ID, "reinstall-review-admin", input, func(context.Context, AgentReinstallEntryCheckResult) string { return "passed" }); err != nil {
		t.Fatal(err)
	}
	before, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.requireReinstallCompletion(ctx, before, node.ID, "reinstall-review-admin", input.OperationID, input.PlanRevision); err == nil {
		t.Fatal("completion accepted without real client proof")
	}
	before.Rollback()
	checks, err := s.QueueAgentReinstallClientChecks(ctx, node.ID, verifier.ID, "reinstall-review-admin", "request-one", input)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 {
		t.Fatalf("checks: %+v", checks)
	}
	again, err := s.QueueAgentReinstallClientChecks(ctx, node.ID, verifier.ID, "reinstall-review-admin", "request-one", input)
	if err != nil || len(again) != 1 || again[0].CommandID != checks[0].CommandID {
		t.Fatalf("not idempotent: %v", err)
	}
	if _, err = s.QueueAgentReinstallClientChecks(ctx, node.ID, verifier.ID, "reinstall-review-admin", "request-two", input); err == nil {
		t.Fatal("overlapping verification accepted")
	}
	task, err := s.claimExecutionTask(ctx, verifier.ID, verifier.Credential, "monitor-registration-evidence-session", 0)
	if err != nil || task == nil || task.MeridianAcceptance == nil {
		t.Fatalf("claim: %+v %v", task, err)
	}
	if task.MeridianAcceptance.Client.Material.ProtocolID != restoreNativeID || task.MeridianAcceptance.Client.Reality.PrivateKey != "" {
		t.Fatal("saved client identity changed or server key exposed")
	}
	digest, err := task.MeridianAcceptance.Digest()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"meridianAcceptance": meridianruntime.AcceptanceResult{TaskDigest: digest}})
	response := submitMonitorInspection(t, s, verifier, task, raw, true)
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	var state, metadata, result string
	if err = s.db.QueryRow(`SELECT c.state,CAST(c.input_json AS TEXT),CAST(a.result_json AS TEXT) FROM application_commands c JOIN agent_reinstall_client_checks a ON a.command_id=c.id WHERE c.id=?`, task.ID).Scan(&state, &metadata, &result); err != nil {
		t.Fatal(err)
	}
	if state != "succeeded" || strings.Contains(metadata, restoreNativeID) || !strings.Contains(result, "checkedAt") {
		t.Fatal("invalid or secret-bearing receipt")
	}
	if blocked, err := agentReinstallBlocked(ctx, s.db, node.ID); err != nil || !blocked {
		t.Fatal("single check released recovery fence")
	}
	views, err := s.AgentReinstallClientChecks(ctx, node.ID)
	if err != nil || len(views) != 1 || views[0].State != "verified" || views[0].AccountName != "Account" {
		t.Fatalf("read view: %+v %v", views, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	evidence, err := s.requireReinstallCompletion(ctx, tx, node.ID, "reinstall-review-admin", input.OperationID, input.PlanRevision)
	if err != nil || len(evidence.Clients[input.ApplicationID]) != 1 || len(evidence.Runtimes) != 1 {
		t.Fatalf("completion gate: %+v %v", evidence.Clients, err)
	}
	var observation []byte
	if err = tx.QueryRow(`SELECT runtime_observation FROM agent_reinstall_app_preparations WHERE application_id=?`, input.ApplicationID).Scan(&observation); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE agent_reinstall_app_preparations SET runtime_observation=X'1234' WHERE application_id=?`, input.ApplicationID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.requireReinstallCompletion(ctx, tx, node.ID, "reinstall-review-admin", input.OperationID, input.PlanRevision); err == nil {
		t.Fatal("corrupt runtime observation accepted")
	}
	if _, err = tx.Exec(`UPDATE agent_reinstall_app_preparations SET runtime_observation=? WHERE application_id=?`, observation, input.ApplicationID); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.readReinstallClientCheck(ctx, tx, verifier.ID, task.ID)
	if err != nil || receipt.State != "verified" {
		t.Fatalf("authenticated evidence: %+v %v", receipt, err)
	}
	if _, err = tx.Exec(`UPDATE task_executions SET sealed_result=X'1234' WHERE task_id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	receipt, err = s.readReinstallClientCheck(ctx, tx, verifier.ID, task.ID)
	if err != nil || receipt.State != "needs_review" {
		t.Fatal("corrupt evidence accepted")
	}
	if _, err = s.requireReinstallCompletion(ctx, tx, node.ID, "reinstall-review-admin", input.OperationID, input.PlanRevision); err == nil {
		t.Fatal("completion accepted corrupt client proof")
	}

	tx.Rollback()
	completeInput := AgentReinstallCompleteInput{OperationID: input.OperationID, PlanRevision: input.PlanRevision}
	if _, err = s.db.Exec(`CREATE TRIGGER fail_recovery_projection BEFORE UPDATE OF status ON services WHEN NEW.status='ready' BEGIN SELECT RAISE(ABORT,'synthetic projection failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteAgentReinstall(ctx, node.ID, "reinstall-review-admin", completeInput); err == nil {
		t.Fatal("injected projection failure accepted")
	}
	if blocked, err := agentReinstallBlocked(ctx, s.db, node.ID); err != nil || !blocked {
		t.Fatal("failed projection released fence")
	}
	if _, err = s.db.Exec(`DROP TRIGGER fail_recovery_projection`); err != nil {
		t.Fatal(err)
	}
	completed, err := s.CompleteAgentReinstall(ctx, node.ID, "reinstall-review-admin", completeInput)
	if err != nil || completed.OperationID != input.OperationID {
		t.Fatalf("finalize: %+v %v", completed, err)
	}
	if blocked, err := agentReinstallBlocked(ctx, s.db, node.ID); err != nil || blocked {
		t.Fatal("completed recovery remains fenced")
	}
	var ready bool
	if err = s.db.QueryRow(`SELECT e.runtime_healthy=1 AND e.status='ready' AND e.applied_revision=e.desired_revision AND d.applied_sha256=evidence.runtime_sha AND service.status='ready' FROM meridian_endpoints e JOIN meridian_deployments d ON d.endpoint_id=e.id JOIN services service ON service.id=e.service_id JOIN (SELECT ? AS runtime_sha) evidence WHERE e.id='restore-endpoint'`, completed.RuntimeDigests[input.ApplicationID]).Scan(&ready); err != nil || !ready {
		t.Fatalf("business projection missing: %v", err)
	}
	againCompleted, err := s.CompleteAgentReinstall(ctx, node.ID, "reinstall-review-admin", completeInput)
	if err != nil || !againCompleted.CompletedAt.Equal(completed.CompletedAt) {
		t.Fatal("lost response repeated finalization")
	}

}
