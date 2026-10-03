package center

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/meridianruntime"
)

func TestAgentReinstallAcceptanceRunsCredentialsSequentially(t *testing.T) {
	for _, success := range []bool{true, false} {
		name := "success advances"
		if !success {
			name = "failure stops"
		}
		t.Run(name, func(t *testing.T) {
			s, node, input := reinstallRuntimeNetworkFixture(t, true, "1.1.1.1")
			ctx := context.Background()
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			protocol := "33333333-3333-4333-8333-333333333333"
			key, err := s.putSecret(ctx, tx, []byte(protocol), meridianCredentialSecretContext("native-second"))
			if err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(`INSERT INTO meridian_credentials(id,account_id,endpoint_id,kind,user_name,identity_sha256,protocol_secret_id,enabled,created_at,updated_at) SELECT 'native-second',account_id,endpoint_id,'native','native-second',?,?,1,created_at,updated_at FROM meridian_credentials WHERE id='native'`, meridian.Identity(protocol), key)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(`INSERT INTO meridian_usage_watermarks(credential_id,observed_bytes,observed_at) SELECT 'native-second',0,observed_at FROM meridian_usage_watermarks WHERE credential_id='native'`); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			plan, err := s.AgentReinstallPlan(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			input.PlanRevision = plan.Revision
			if _, err = s.QueueAgentReinstallRuntime(ctx, node.ID, "reinstall-review-admin", input); err != nil {
				t.Fatal(err)
			}
			runtime, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
			if err != nil || runtime == nil {
				t.Fatal(err)
			}
			if response := submitRestoredRuntime(t, s, node, runtime, true, true); response.Code != http.StatusOK {
				t.Fatal(response.Body.String())
			}
			plan, err = s.AgentReinstallPlan(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			input.PlanRevision = plan.Revision
			if _, err = s.ActivateAgentReinstallAccess(ctx, node.ID, "reinstall-review-admin", input); err != nil {
				t.Fatal(err)
			}
			enrollment, err := s.CreateAgentEnrollment(ctx, AgentEnrollmentSpec{SiteID: testSiteID(t, s), Name: "verifier", CenterURL: "https://center.example.com"})
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
			plan, err = s.AgentReinstallPlan(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			input.PlanRevision = plan.Revision
			checks, err := s.QueueAgentReinstallClientChecks(ctx, node.ID, verifier.ID, "reinstall-review-admin", "sequence-request", input)
			if err != nil || len(checks) != 1 {
				t.Fatalf("queue: %v %v", checks, err)
			}
			first, err := s.claimExecutionTask(ctx, verifier.ID, verifier.Credential, "monitor-registration-evidence-session", 0)
			if err != nil || first == nil {
				t.Fatal(err)
			}
			digest, err := first.MeridianAcceptance.Digest()
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"meridianAcceptance": meridianruntime.AcceptanceResult{TaskDigest: digest}})
			if response := submitMonitorInspection(t, s, verifier, first, raw, success); response.Code != http.StatusOK {
				t.Fatal(response.Body.String())
			}
			var active, total int
			if err = s.db.QueryRow(`SELECT COUNT(*),SUM(state IN ('pending','running')) FROM application_commands WHERE kind='meridian.recovery.acceptance'`).Scan(&total, &active); err != nil {
				t.Fatal(err)
			}
			if !success {
				if total != 1 || active != 0 {
					t.Fatalf("failure advanced: %d %d", total, active)
				}
				return
			}
			if total != 2 || active != 1 {
				t.Fatalf("next task missing or concurrent: %d %d", total, active)
			}
			second, err := s.claimExecutionTask(ctx, verifier.ID, verifier.Credential, "monitor-registration-evidence-session", 0)
			if err != nil || second == nil {
				t.Fatal(err)
			}
			if second.MeridianAcceptance.Client.Material.Credential.ID == first.MeridianAcceptance.Client.Material.Credential.ID {
				t.Fatal("repeated credential")
			}
			digest, err = second.MeridianAcceptance.Digest()
			if err != nil {
				t.Fatal(err)
			}
			raw, _ = json.Marshal(map[string]any{"meridianAcceptance": meridianruntime.AcceptanceResult{TaskDigest: digest}})
			if response := submitMonitorInspection(t, s, verifier, second, raw, true); response.Code != http.StatusOK {
				t.Fatal(response.Body.String())
			}
			if err = s.db.QueryRow(`SELECT COUNT(*) FROM application_commands WHERE kind='meridian.recovery.acceptance' AND state IN ('pending','running')`).Scan(&active); err != nil || active != 0 {
				t.Fatalf("unfinished: %d %v", active, err)
			}
			views, err := s.AgentReinstallClientChecks(ctx, node.ID)
			if err != nil || len(views) != 2 {
				t.Fatalf("receipts: %v %v", views, err)
			}
			for _, view := range views {
				if view.State != "verified" {
					t.Fatalf("invalid receipt: %+v", view)
				}
			}
		})
	}
}
