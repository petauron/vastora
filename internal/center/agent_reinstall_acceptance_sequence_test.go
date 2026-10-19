package center

import (
	"context"
	"encoding/json"

	"testing"
	"time"

	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/meridianruntime"
)

func TestRetiredAcceptanceCannotAdvanceToAnotherCredential(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(map[bool]string{false: "late-failure", true: "late-success"}[success], func(t *testing.T) {
			s, node, input := reinstallRuntimeNetworkFixture(t, true, "1.1.1.1")
			ctx := context.Background()
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			protocol := "33333333-3333-4333-8333-333333333333"
			key, err := s.putSecret(ctx, tx, []byte(protocol), meridianCredentialSecretContext("native-second"))
			if err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if _, err := tx.Exec(`INSERT INTO meridian_credentials(id,account_id,endpoint_id,kind,user_name,identity_sha256,protocol_secret_id,enabled,created_at,updated_at) SELECT 'native-second',account_id,endpoint_id,'native','native-second',?,?,1,created_at,updated_at FROM meridian_credentials WHERE id='native'`, glivela.Identity(protocol), key); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if _, err := tx.Exec(`INSERT INTO meridian_usage_watermarks(credential_id,observed_bytes,observed_at) SELECT 'native-second',0,observed_at FROM meridian_usage_watermarks WHERE credential_id='native'`); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			plan, err := s.AgentReinstallPlan(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			input.PlanRevision = plan.Revision
			seedSuccessfulHistoricalRuntime(t, s, node, input)
			plan, err = s.AgentReinstallPlan(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			input.PlanRevision = plan.Revision
			if _, err := s.ActivateAgentReinstallAccess(ctx, node.ID, "reinstall-review-admin", input); err != nil {
				t.Fatal(err)
			}
			enrollment, err := s.CreateAgentEnrollment(ctx, AgentEnrollmentSpec{SiteID: testSiteID(t, s), Name: "verifier", CenterURL: "https://center.example.com"})
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
			plan, err = s.AgentReinstallPlan(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			input.PlanRevision = plan.Revision
			checks, err := s.QueueAgentReinstallClientChecks(ctx, node.ID, verifier.ID, "reinstall-review-admin", "sequence-request", input)
			if err != nil || len(checks) != 1 {
				t.Fatalf("historical first check: %v", err)
			}
			tx, err = s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			targets, err := s.reinstallAcceptanceTasks(ctx, tx, node.ID, verifier.ID, "reinstall-review-admin", input)
			_ = tx.Rollback()
			if err != nil || len(targets) != 2 || targets[0].Client.Material.Credential.ID == targets[1].Client.Material.Credential.ID {
				t.Fatalf("distinct retained credentials missing: %v", err)
			}
			task, err := s.claimExecutionTask(ctx, verifier.ID, verifier.Credential, "monitor-registration-evidence-session", 0)
			if err != nil || task != nil {
				t.Fatalf("old credential check dispatched: %v %v", task, err)
			}
			var credential string
			if err := s.db.QueryRow(`SELECT json_extract(input_json,'$.credentialId') FROM agent_reinstall_client_checks WHERE command_id=?`, checks[0].CommandID).Scan(&credential); err != nil {
				t.Fatal(err)
			}
			digest := ""
			for _, target := range targets {
				if target.Client.Material.Credential.ID == credential {
					digest, err = target.Digest()
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if digest == "" {
				t.Fatal("retained check lost its credential binding")
			}
			if _, err := s.db.Exec(`UPDATE application_commands SET state='running',attempt=1 WHERE id=?`, checks[0].CommandID); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"meridianAcceptance": meridianruntime.AcceptanceResult{TaskDigest: digest}})
			assertRetiredApplicationResultPreservesState(t, s, verifier.ID, checks[0].CommandID, success, raw)
			var total, active int
			if err := s.db.QueryRow(`SELECT COUNT(*),SUM(state IN ('pending','running')) FROM application_commands WHERE kind='meridian.recovery.acceptance'`).Scan(&total, &active); err != nil || total != 1 || active != 1 {
				t.Fatal("late result advanced or rewrote the retired pipeline")
			}
			views, err := s.AgentReinstallClientChecks(ctx, node.ID)
			if err != nil || len(views) != 1 || views[0].State == "verified" {
				t.Fatalf("historical receipt became fresh proof: %v", err)
			}
		})
	}
}
