package center

import (
	"context"
	"encoding/json"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/meridianruntime"

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

func TestRetiredAgentReinstallAcceptanceCannotProjectOrReleaseRecovery(t *testing.T) {
	s, node, input := reinstallEntryCheckFixture(t, "1.1.1.1")
	ctx := context.Background()
	if _, err := s.ActivateAgentReinstallAccess(ctx, node.ID, "reinstall-review-admin", input); err != nil {
		t.Fatal(err)
	}
	enrollment, err := s.CreateAgentEnrollment(ctx, AgentEnrollmentSpec{SiteID: testSiteID(t, s), Name: "acceptance-verifier", CenterURL: "https://center.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := s.EnrollAgent(ctx, enrollment.Token, Version, "linux", "amd64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE agents SET capabilities_json='{"docker":true,"meridianAcceptance":true}',last_seen_at=? WHERE id=?`, s.now().UTC().Format(time.RFC3339Nano), verifier.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterExecutionSession(ctx, verifier.ID, verifier.Credential, "monitor-registration-evidence-session", controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.PlanRevision = plan.Revision
	if _, err := s.verifyAgentReinstallEntry(ctx, node.ID, "reinstall-review-admin", input, func(context.Context, AgentReinstallEntryCheckResult) string { return "passed" }); err != nil {
		t.Fatal(err)
	}
	checks, err := s.QueueAgentReinstallClientChecks(ctx, node.ID, verifier.ID, "reinstall-review-admin", "request-one", input)
	if err != nil || len(checks) != 1 {
		t.Fatalf("historical check intent: %v", err)
	}
	repeated, err := s.QueueAgentReinstallClientChecks(ctx, node.ID, verifier.ID, "reinstall-review-admin", "request-one", input)
	if err != nil || len(repeated) != 1 || repeated[0].CommandID != checks[0].CommandID {
		t.Fatal("historical check identity changed")
	}
	if _, err := s.QueueAgentReinstallClientChecks(ctx, node.ID, verifier.ID, "reinstall-review-admin", "request-two", input); err == nil {
		t.Fatal("overlapping old check was queued")
	}
	task, err := s.claimExecutionTask(ctx, verifier.ID, verifier.Credential, "monitor-registration-evidence-session", 0)
	if err != nil || task != nil {
		t.Fatalf("retired client check dispatched: %v %v", task, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := s.reinstallAcceptanceTasks(ctx, tx, node.ID, verifier.ID, "reinstall-review-admin", input)
	_ = tx.Rollback()
	if err != nil || len(targets) != 1 {
		t.Fatalf("historical target identity: %v", err)
	}
	if targets[0].Client.Material.ProtocolID != restoreNativeID || targets[0].Client.Reality.PrivateKey != "" {
		t.Fatal("saved client identity changed or server key exposed")
	}
	digest, err := targets[0].Digest()
	if err != nil {
		t.Fatal(err)
	}
	// Model an attempt issued before upgrade, never issue a new legacy command.
	if _, err := s.db.Exec(`UPDATE application_commands SET state='running',attempt=1 WHERE id=?`, checks[0].CommandID); err != nil {
		t.Fatal(err)
	}
	valid, _ := json.Marshal(map[string]any{"meridianAcceptance": meridianruntime.AcceptanceResult{TaskDigest: digest}})
	malformed, _ := json.Marshal(map[string]any{"meridianAcceptance": meridianruntime.AcceptanceResult{TaskDigest: "changed"}})
	for _, raw := range []json.RawMessage{valid, malformed, json.RawMessage(`{}`)} {
		for _, success := range []bool{false, true} {
			assertRetiredApplicationResultPreservesState(t, s, verifier.ID, checks[0].CommandID, success, raw)
		}
	}
	var metadata string
	if err := s.db.QueryRow(`SELECT CAST(input_json AS TEXT) FROM application_commands WHERE id=?`, checks[0].CommandID).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(metadata, restoreNativeID) || strings.Contains(metadata, "original-private-key") {
		t.Fatal("recovery metadata contains credentials")
	}
	views, err := s.AgentReinstallClientChecks(ctx, node.ID)
	if err != nil || len(views) != 1 || views[0].State == "verified" {
		t.Fatalf("late receipt became fresh acceptance: %v", err)
	}
	if blocked, err := agentReinstallBlocked(ctx, s.db, node.ID); err != nil || !blocked {
		t.Fatal("legacy proof released recovery fence")
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, completionErr := s.requireReinstallCompletion(ctx, tx, node.ID, "reinstall-review-admin", input.OperationID, input.PlanRevision)
	_ = tx.Rollback()
	if completionErr == nil {
		t.Fatal("legacy client proof finalized application-owned restoration")
	}
}
