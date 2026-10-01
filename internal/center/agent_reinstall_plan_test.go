package center

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/networking"
)

func reinstallPlanFixture(t *testing.T) (*Store, AgentCredential) {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	enrollment, err := store.CreateAgentEnrollment(context.Background(), AgentEnrollmentSpec{SiteID: testSiteID(t, store), Name: "Node A", CenterURL: "https://center.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.EnrollAgent(context.Background(), enrollment.Token, Version, "linux", "amd64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	return store, node
}

func addReinstallApplication(t *testing.T, store *Store, node AgentCredential, id, appKey, version, operation, state string) {
	t.Helper()
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	_, manifestID, _ := strings.Cut(appKey, "/")
	manifest, err := json.Marshal(catalog.AppManifest{ID: manifestID, Version: version, Name: catalog.LocalizedText{English: "App", SimplifiedChinese: "应用"},
		Description: catalog.LocalizedText{English: "Recovery fixture", SimplifiedChinese: "恢复夹具"}, License: "MIT",
		Images: []catalog.Image{{Name: "runtime", Reference: "ghcr.io/example/app@sha256:" + strings.Repeat("a", 64)}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,status,runtime,created_at,updated_at)
		VALUES(?,?,?,?,?,'running','docker',?,?)`, id, id, node.ID, testSiteID(t, store), appKey, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO deployments(id,agent_id,app_key,app_version,manifest_json,config_json,operation,state,created_at,updated_at,application_id)
		VALUES(?,?,?,?,?,? ,?,?,?,?,?)`, id+"-deployment", node.ID, appKey, version, manifest, []byte(`{"sensitive":"do-not-return-config"}`), operation, state, stamp, stamp, id); err != nil {
		t.Fatal(err)
	}
}

func TestAgentReinstallPlanKeepsSavedVersionsAndRecoveryRequirements(t *testing.T) {
	store, node := reinstallPlanFixture(t)
	cases := []struct{ id, key, version, recovery, requirement string }{
		{"meridian", meridianAppKey, "0.1.0-alpha.12", "rebuild_configuration", "validate_saved_meridian_intent_and_credentials"},
		{"collector", pulseAgentAppKey, "0.1.0-alpha.5", "reenroll_monitor", "replace_managed_monitor_enrollment"},
		{"monitor", pulseAppKey, "0.1.0-alpha.5", "restore_data", "restore_backup_on_replacement"},
		{"cpa", "vastora-official/cpa", "7.2.130", "restore_data", "restore_backup_on_replacement"},
		{"custom", "external/custom", "1.0.0", "restore_data", "restore_backup_on_replacement"},
	}
	for _, test := range cases {
		addReinstallApplication(t, store, node, test.id, test.key, test.version, "install", "succeeded")
	}
	plan, err := store.AgentReinstallPlan(context.Background(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		index := slices.IndexFunc(plan.Applications, func(app AgentReinstallApplication) bool { return app.ApplicationID == test.id })
		if index < 0 {
			t.Fatalf("missing %s", test.id)
		}
		app := plan.Applications[index]
		if app.Version != test.version || app.Recovery != test.recovery || !slices.Contains(app.Requirements, test.requirement) {
			t.Fatalf("wrong saved recovery requirement: %+v", app)
		}
	}
	if len(plan.IdentityFingerprint) != 64 || plan.CredentialRevoked || !slices.Contains(plan.Requirements, "verify_business_before_completion") {
		t.Fatalf("identity or verification requirements missing: %+v", plan)
	}
}

func TestAgentReinstallPlanDoesNotResurrectUninstallOrHidePendingWork(t *testing.T) {
	store, node := reinstallPlanFixture(t)
	addReinstallApplication(t, store, node, "app", meridianAppKey, "0.1.0-alpha.12", "install", "succeeded")
	// Historical success was updated later, but the later *intent* is uninstall.
	if _, err := store.db.Exec(`UPDATE deployments SET updated_at='2099-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO deployments(id,agent_id,app_key,app_version,manifest_json,config_json,operation,state,created_at,updated_at,application_id)
		SELECT 'pending-uninstall',agent_id,app_key,app_version,manifest_json,config_json,'uninstall','pending',created_at,created_at,application_id FROM deployments`); err != nil {
		t.Fatal(err)
	}
	plan, err := store.AgentReinstallPlan(context.Background(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	app := plan.Applications[0]
	if app.DeploymentID != "pending-uninstall" || app.Recovery != "keep_stopped" || !slices.Contains(app.Requirements, "inspect_previous_operation") {
		t.Fatalf("uninstall intent lost: %+v", app)
	}
	if len(plan.PendingWork) != 1 || plan.PendingWork[0].Kind != "application.apply" || plan.PendingWork[0].Count != 1 || len(plan.Executions) != 0 {
		t.Fatalf("unclaimed work lost: %+v", plan)
	}
	var state string
	var attempt int
	if err := store.db.QueryRow(`SELECT state,attempt FROM deployments WHERE id='pending-uninstall'`).Scan(&state, &attempt); err != nil || state != "pending" || attempt != 0 {
		t.Fatalf("review changed command state: %q %d %v", state, attempt, err)
	}
}

func TestAgentReinstallPlanRequiresReviewOfMissingOrInvalidArtifacts(t *testing.T) {
	for _, mode := range []string{"missing", "invalid", "mismatched-version", "mismatched-app"} {
		t.Run(mode, func(t *testing.T) {
			store, node := reinstallPlanFixture(t)
			addReinstallApplication(t, store, node, "app", meridianAppKey, "0.1.0-alpha.12", "install", "succeeded")
			query := map[string]string{
				"missing":            `DELETE FROM deployments`,
				"invalid":            `UPDATE deployments SET manifest_json='{}'`,
				"mismatched-version": `UPDATE deployments SET app_version='0.1.0-alpha.13'`,
				"mismatched-app":     `UPDATE deployments SET manifest_json=json_set(manifest_json,'$.id','different')`,
			}[mode]
			if _, err := store.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			plan, err := store.AgentReinstallPlan(context.Background(), node.ID)
			if err != nil || plan.Applications[0].Recovery != "review_required" {
				t.Fatalf("invalid saved artifact accepted: %+v %v", plan, err)
			}
		})
	}
}

func TestAgentReinstallPlanRetainsNetworkAndUncertainEvidence(t *testing.T) {
	store, node := reinstallPlanFixture(t)
	profile, _ := json.Marshal(networking.Profile{ServiceAddress: "100.64.0.7", HeadscaleAddress: "100.64.0.7", EnabledKinds: []string{networking.KindHeadscale}})
	if _, err := store.db.Exec(`INSERT INTO agent_network_profile_recovery(agent_id,public_key,profile_json) VALUES(?,X'1234',?)`, node.ID, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agents SET tailscale_ownership='managed' WHERE id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,sealed_result,state,phase,expires_at,created_at,updated_at,identity_retired_at)
		VALUES('old-execution',?,'old-task','application.command',1,'old-session','digest',X'1234',X'5678','unknown','result_received',?,?,?,?)`, node.ID, stamp, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	plan, err := store.AgentReinstallPlan(context.Background(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.PrivateNetwork.ProfileRetained || plan.PrivateNetwork.PrivateAddress != "100.64.0.7" || plan.PrivateNetwork.AddressRecovery != "explicit_migration_required" {
		t.Fatalf("lost retained network dependency: %+v", plan.PrivateNetwork)
	}
	if len(plan.Executions) != 1 || !plan.Executions[0].IdentityRetired || plan.Executions[0].State != "unknown" || plan.Executions[0].Phase != "result_received" {
		t.Fatalf("lost uncertain execution: %+v", plan.Executions)
	}
	if _, err := store.db.Exec(`UPDATE agents SET tailscale_ownership='external' WHERE id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	plan, err = store.AgentReinstallPlan(context.Background(), node.ID)
	if err != nil || plan.PrivateNetwork.AddressRecovery != "operator_managed" {
		t.Fatalf("external network ownership: %+v %v", plan, err)
	}
}

func TestAgentReinstallPlanHTTPReadOnlyAndNoSecrets(t *testing.T) {
	store, node := reinstallPlanFixture(t)
	addReinstallApplication(t, store, node, "app", meridianAppKey, "0.1.0-alpha.12", "install", "succeeded")
	session, _, err := store.CreateFirstAdmin(context.Background(), "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(store, "", false).Handler()
	for _, test := range []struct {
		id            string
		authenticated bool
		status        int
	}{{node.ID, false, 401}, {node.ID, true, 200}, {"missing-node", true, 404}} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/agents/"+test.id+"/reinstall-plan", nil)
		if test.authenticated {
			r.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("status=%d: %s", w.Code, w.Body.String())
		}
		if w.Code == 200 {
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("review may be cached")
			}
			for _, forbidden := range []string{node.Credential, "do-not-return-config", "sealedTask", "sealedResult", "publicKey", "manifest_json"} {
				if strings.Contains(w.Body.String(), forbidden) {
					t.Fatalf("review leaks %q", forbidden)
				}
			}
		}
	}
	if err := store.authenticateAgent(context.Background(), node.ID, node.Credential); err != nil {
		t.Fatalf("review revoked Agent: %v", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM deployments`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("review queued work: %d %v", count, err)
	}
}

func TestAgentReinstallPlanScopesWorkAndIngressDependencies(t *testing.T) {
	store, node := reinstallPlanFixture(t)
	enrollment, err := store.CreateAgentEnrollment(context.Background(), AgentEnrollmentSpec{SiteID: testSiteID(t, store), Name: "Node B", CenterURL: "https://center.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.EnrollAgent(context.Background(), enrollment.Token, Version, "linux", "amd64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	addReinstallApplication(t, store, other, "other-app", meridianAppKey, "0.1.0-alpha.12", "install", "pending")
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO services(id,application_id,site_id,name,protocol,app_protocol,container_port,host_port,endpoint,source,status,created_at,updated_at)
		VALUES('other-service','other-app',?,'web','http','http',8080,8080,'http://10.0.0.8:8080','catalog','ready',?,?)`, testSiteID(t, store), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,dns_provider,status,created_at,updated_at)
		VALUES('shared-ingress','other-service','lan_gateway','site_gateway',?,'app.example.test','manual','ready',?,?)`, node.ID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	plan, err := store.AgentReinstallPlan(context.Background(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Applications)+len(plan.PendingWork)+len(plan.Executions) != 0 || plan.PrivateNetwork.Publications != 1 || !slices.Contains(plan.Requirements, "rebuild_and_verify_access_entries") {
		t.Fatalf("review crossed node scope or missed shared ingress: %+v", plan)
	}
}
