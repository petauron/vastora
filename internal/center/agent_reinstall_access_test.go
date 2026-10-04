package center

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/networking"
)

func TestAgentReinstallAccessActivatesOnlyReviewedBinding(t *testing.T) {
	s, node, input := reinstallEntryCheckFixture(t)
	ctx := context.Background()
	before, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	var secrets, credentials string
	if err = s.db.QueryRow(`SELECT (SELECT group_concat(hex(sealed)) FROM secrets),(SELECT group_concat(json_array(id,enabled,egress_node_id)) FROM meridian_credentials)`).Scan(&secrets, &credentials); err != nil {
		t.Fatal(err)
	}
	result, err := s.ActivateAgentReinstallAccess(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || result.State != "applied" || result.ServiceAddress != "10.0.0.8" || result.PublicAddress != "198.51.100.8" {
		t.Fatalf("activate: %+v %v", result, err)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.NetworkReview.ProfileActive || !plan.NetworkReview.ApprovalCurrent || !reflect.DeepEqual(before.NetworkReview.Approval, plan.NetworkReview.Approval) || !reflect.DeepEqual(before.NetworkReview.Previous, plan.NetworkReview.Previous) || plan.Revision == before.Revision {
		t.Fatalf("network review lost original evidence: %+v", plan.NetworkReview)
	}
	if plan.Applications[0].Preparation.Access == nil || plan.Applications[0].Preparation.Access.State != "applied" || plan.Recovery.State != "review_required" {
		t.Fatalf("missing receipt: %+v", plan.Applications[0].Preparation)
	}
	profile, err := networkProfile(ctx, s.db, node.ID)
	if err != nil || profile == nil || profile.ServiceAddress != result.ServiceAddress {
		t.Fatalf("profile: %+v %v", profile, err)
	}
	var endpoint, address, serviceState, publicationState, currentSecrets, currentCredentials string
	var healthy, applied, desired, recoveryRows int
	if err = s.db.QueryRow(`SELECT s.endpoint,e.listen_address,s.status,p.status,e.runtime_healthy,e.applied_revision,e.desired_revision,(SELECT COUNT(*) FROM agent_network_profile_recovery WHERE agent_id=?),(SELECT group_concat(hex(sealed)) FROM secrets),(SELECT group_concat(json_array(id,enabled,egress_node_id)) FROM meridian_credentials) FROM services s JOIN meridian_endpoints e ON e.service_id=s.id JOIN publications p ON p.service_id=s.id WHERE s.id='restore-service'`, node.ID).Scan(&endpoint, &address, &serviceState, &publicationState, &healthy, &applied, &desired, &recoveryRows, &currentSecrets, &currentCredentials); err != nil {
		t.Fatal(err)
	}
	if endpoint != "10.0.0.8:10443" || address != "10.0.0.8" || serviceState != "degraded" || publicationState != "degraded" || healthy != 0 || applied == desired || recoveryRows != 0 || secrets != currentSecrets || credentials != currentCredentials {
		t.Fatalf("unexpected business changes: %s %s %s %s %d %d/%d %d", endpoint, address, serviceState, publicationState, healthy, applied, desired, recoveryRows)
	}
	if _, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0); !errors.Is(err, errExecutionBlocked) {
		t.Fatalf("fence released: %v", err)
	}
	// Address projection must preserve the approved task digests. No new task is
	// issued by activation or by retrieving its receipt after a lost response.
	again, err := s.ActivateAgentReinstallAccess(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || again != result {
		t.Fatalf("repeat: %+v %v", again, err)
	}
	if _, err = s.QueueAgentReinstallListener(ctx, node.ID, "reinstall-review-admin", input); err != nil {
		t.Fatalf("changed applied listener: %v", err)
	}
	var activations, events int
	if err = s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM agent_reinstall_access_activations),(SELECT COUNT(*) FROM task_events WHERE kind='agent.reinstall.access')`).Scan(&activations, &events); err != nil || activations != 1 || events != 1 {
		t.Fatalf("duplicate side effects: %d %d %v", activations, events, err)
	}
	// A verification approved for the old address projection cannot be reused.
	if _, err = s.verifyAgentReinstallEntry(ctx, node.ID, "reinstall-review-admin", input, func(context.Context, AgentReinstallEntryCheckResult) string { t.Fatal("stale probe"); return "passed" }); err == nil {
		t.Fatal("old review reused")
	}
	input.PlanRevision = plan.Revision
	if _, err = s.verifyAgentReinstallEntry(ctx, node.ID, "reinstall-review-admin", input, func(context.Context, AgentReinstallEntryCheckResult) string { return "passed" }); err != nil {
		t.Fatalf("fresh public check blocked: %v", err)
	}
	directory, clock := s.dataDir, s.now
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.now = clock
	again, err = s.ActivateAgentReinstallAccess(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || again != result {
		t.Fatalf("restart: %+v %v", again, err)
	}
}

func TestAgentReinstallAccessWithoutSharedEntry(t *testing.T) {
	s, node, input := reinstallRuntimeFixture(t)
	ctx := context.Background()
	if _, err := s.QueueAgentReinstallRuntime(ctx, node.ID, "reinstall-review-admin", input); err != nil {
		t.Fatal(err)
	}
	task, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}
	if response := submitRestoredRuntime(t, s, node, task, true, true); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.PlanRevision = plan.Revision
	result, err := s.ActivateAgentReinstallAccess(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || result.State != "applied" || result.PublicAddress != "" {
		t.Fatalf("private activation: %+v %v", result, err)
	}
}

func TestAgentReinstallAccessRejectsChangedOrUnreviewedTargets(t *testing.T) {
	for _, mode := range []string{"admin", "review", "operation", "identity", "runtime", "listener", "service", "network", "profile"} {
		t.Run(mode, func(t *testing.T) {
			s, node, input := reinstallEntryCheckFixture(t)
			admin := "reinstall-review-admin"
			query := ""
			switch mode {
			case "admin":
				admin = "other-admin"
			case "review":
				input.PlanRevision = strings.Repeat("f", 64)
			case "operation":
				input.OperationID = "other-operation"
			case "identity":
				query = `UPDATE agents SET x25519_public_key=randomblob(32)`
			case "runtime":
				query = `UPDATE application_commands SET state='failed' WHERE id LIKE 'reinstall-runtime-%'`
			case "listener":
				query = `UPDATE node_listener_states SET status='failed'`
			case "service":
				query = `UPDATE services SET status='stopped'`
			case "network":
				query = `UPDATE agent_reinstall_network_approvals SET approval_json=json_set(approval_json,'$.profile.serviceAddress','10.0.0.9')`
			case "profile":
				tx, err := s.db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				plan, err := s.agentReinstallPlan(context.Background(), tx, node.ID)
				if err != nil {
					t.Fatal(err)
				}
				profile := plan.NetworkReview.Approval.Profile
				profile.ServiceAddress = "10.0.0.9"
				if err = saveNetworkProfile(context.Background(), tx, node.ID, profile); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if query != "" {
				if _, err := s.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.ActivateAgentReinstallAccess(context.Background(), node.ID, admin, input); err == nil {
				t.Fatal("accepted changed authority")
			}
			var count int
			var endpoint string
			if err := s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM agent_reinstall_access_activations),endpoint FROM services WHERE id='restore-service'`).Scan(&count, &endpoint); err != nil || count != 0 || endpoint != "10.0.0.7:10443" {
				t.Fatalf("partial activation: %d %s %v", count, endpoint, err)
			}
		})
	}
}

func TestAgentReinstallAccessRollbackPreservesOriginalProfileAndBinding(t *testing.T) {
	s, node, input := reinstallEntryCheckFixture(t)
	if _, err := s.db.Exec(`CREATE TRIGGER fail_access BEFORE INSERT ON agent_reinstall_access_activations BEGIN SELECT RAISE(ABORT,'activation receipt failed'); END`); err != nil {
		t.Fatal(err)
	}
	before, err := s.AgentReinstallPlan(context.Background(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ActivateAgentReinstallAccess(context.Background(), node.ID, "reinstall-review-admin", input); err == nil {
		t.Fatal("expected receipt failure")
	}
	after, err := s.AgentReinstallPlan(context.Background(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Revision != after.Revision || after.NetworkReview.ProfileActive || !reflect.DeepEqual(before.NetworkReview.Previous, after.NetworkReview.Previous) {
		t.Fatal("failed activation changed prior review")
	}
	var address string
	if err = s.db.QueryRow(`SELECT listen_address FROM meridian_endpoints WHERE id='restore-endpoint'`).Scan(&address); err != nil || address != "10.0.0.7" {
		t.Fatalf("rollback lost binding: %s %v", address, err)
	}
}

func TestAgentReinstallAccessChangedBindingRequiresReviewWithoutReapply(t *testing.T) {
	for _, mode := range []string{"profile", "service", "endpoint", "listener"} {
		t.Run(mode, func(t *testing.T) {
			s, node, input := reinstallEntryCheckFixture(t)
			ctx := context.Background()
			if _, err := s.ActivateAgentReinstallAccess(ctx, node.ID, "reinstall-review-admin", input); err != nil {
				t.Fatal(err)
			}
			query := map[string]string{"profile": `UPDATE agent_network_profiles SET service_address='10.0.0.9'`, "service": `UPDATE services SET endpoint='10.0.0.9:10443'`, "endpoint": `UPDATE meridian_endpoints SET listen_address='10.0.0.9'`, "listener": `UPDATE node_listener_states SET status='failed'`}[mode]
			if _, err := s.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			plan, err := s.AgentReinstallPlan(ctx, node.ID)
			if err != nil || plan.Applications[0].Preparation.Access.State != "needs_review" {
				t.Fatalf("changed binding not shown: %v", err)
			}
			if _, err = s.ActivateAgentReinstallAccess(ctx, node.ID, "reinstall-review-admin", input); err == nil {
				t.Fatal("reapplied changed binding")
			}
		})
	}
}

func TestAgentReinstallAccessDoesNotAuthorizeOrdinaryDNS(t *testing.T) {
	s, node, input := reinstallEntryCheckFixture(t)
	ctx := context.Background()
	if _, err := s.ActivateAgentReinstallAccess(ctx, node.ID, "reinstall-review-admin", input); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"manual", "cloudflare", "headscale"} {
		if _, err := s.reconcilePublicationDNS(ctx, "restored-entry", node.ID, provider, 1); err == nil || !strings.Contains(err.Error(), "active reinstall recovery") {
			t.Fatalf("ordinary %s DNS escaped recovery: %v", provider, err)
		}
	}
	var dns, status string
	if err := s.db.QueryRow(`SELECT dns_record_id,status FROM publications WHERE id='restored-entry'`).Scan(&dns, &status); err != nil || dns != "" || status != "degraded" {
		t.Fatalf("DNS or publication changed: %s %s %v", dns, status, err)
	}
}

func TestAgentReinstallAccessCannotEnterUnrelatedHeadscaleDNSSnapshot(t *testing.T) {
	s, node, input := reinstallEntryCheckFixture(t)
	ctx := context.Background()
	if _, err := s.ActivateAgentReinstallAccess(ctx, node.ID, "reinstall-review-admin", input); err != nil {
		t.Fatal(err)
	}
	configureBuiltinHeadscaleForTest(t, s)
	other := enrollOrchestrationNode(t, s, "unrelated", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "100.64.0.40", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.40", HeadscaleAddress: "100.64.0.40", EnabledKinds: []string{networking.KindHeadscale}})
	for _, statement := range []string{
		`UPDATE publications SET kind='headscale_gateway',ingress_owner='site_gateway',dns_provider='headscale' WHERE id='restored-entry'`,
		`UPDATE agent_network_profiles SET headscale_address='100.64.0.8' WHERE service_address='10.0.0.8'`,
		`INSERT INTO applications(id,name,node_id,site_id,app_key,status,created_at,updated_at) SELECT 'other-app','Other',?,site_id,'test/other','running',created_at,updated_at FROM applications WHERE id=(SELECT application_id FROM services WHERE id='restore-service')`,
		`INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,app_protocol,status,created_at,updated_at) SELECT 'other-service','other-app',site_id,'web','tcp',8080,8080,'100.64.0.40:8080','observed','http','ready',created_at,updated_at FROM services WHERE id='restore-service'`,
		`INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,dns_provider,status,created_at,updated_at) SELECT 'other-entry','other-service','headscale_gateway','site_gateway',?,'other.example.test','headscale','ready',created_at,updated_at FROM publications WHERE id='restored-entry'`,
	} {
		var err error
		if strings.Contains(statement, "?") {
			_, err = s.db.Exec(statement, other.ID)
		} else {
			_, err = s.db.Exec(statement)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.reconcileHeadscaleDNS(ctx); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(filepath.Join(s.dataDir, headscaleDNSFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "entry.example.test") || strings.Contains(string(encoded), "100.64.0.8") || !strings.Contains(string(encoded), "other.example.test") || !strings.Contains(string(encoded), "100.64.0.40") {
		t.Fatalf("incorrect DNS snapshot: %s", encoded)
	}
}
