package center

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/senulun"
)

func senulunFixture(t *testing.T) (*Store, AgentCredential, AgentCredential, DeploymentView) {
	t.Helper()
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	service := enrollOrchestrationNode(t, store, "monitor", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.10", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.10", LANAddress: "10.0.0.10", EnabledKinds: []string{networking.KindLAN}})
	collector := enrollOrchestrationNode(t, store, "collector", NodeCapabilities{}, []networking.Candidate{{Address: "10.0.0.11", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.11", LANAddress: "10.0.0.11", EnabledKinds: []string{networking.KindLAN}})
	deployment, err := store.CreateDeployment(context.Background(), DeploymentRequest{AgentID: service.ID, AppKey: senulunAppKey, Config: json.RawMessage(`{"public_url":"https://senulun.example.com"}`)})
	if err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, service, "application.apply", json.RawMessage(`{"services":[{"name":"dashboard","protocol":"http","containerPort":8080,"hostPort":18080,"address":"10.0.0.10"}]}`))
	return store, service, collector, deployment
}

func addSenulunPrivateFixture(t *testing.T, store *Store, applicationID, nodeID string) {
	t.Helper()
	_, err := store.db.Exec(`INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,dns_provider,tls_enabled,status,created_at,updated_at)
		SELECT 'pulse-private',id,'lan_gateway','site_gateway',?,'senulun.private.example.com','manual',1,'ready','','' FROM services WHERE application_id = ? AND name = 'dashboard'`, nodeID, applicationID)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSenulunAgentInstallsAgainstIndependentService(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	collector := enrollOrchestrationNode(t, store, "collector", NodeCapabilities{}, []networking.Candidate{{Address: "10.0.0.11", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.11", LANAddress: "10.0.0.11", EnabledKinds: []string{networking.KindLAN}})
	ctx := context.Background()
	request := DeploymentRequest{AgentID: collector.ID, AppKey: senulunAgentAppKey, Config: json.RawMessage(`{"service_url":"https://independent.example.test/","node_name":"my-node","node_group":"servers","node_region":"SG","geoip_provider":"disabled","enrollment_token":"test-test-test-test"}`)}
	installed, err := store.CreateDeployment(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	installTask := claimTask(t, store, collector)
	if installTask.AppKey != senulunAgentAppKey || !strings.Contains(string(installTask.Secrets), "test-test-test-test") || strings.Contains(string(installTask.Config), "test-test-test-test") {
		t.Fatal("manual enrollment token was not delivered as a private package secret")
	}
	var config senulun.AgentConfig
	if json.Unmarshal(installTask.Config, &config) != nil || config.NodeName != "my-node" || config.ServiceURL != "https://independent.example.test/" || config.ServiceApplicationID != "" {
		t.Fatal("installation depended on a Center-managed monitoring service")
	}
	if config.NodeRegion == nil || *config.NodeRegion != "SG" || config.GeoIPProvider == nil || *config.GeoIPProvider != "disabled" {
		t.Fatal("administrator location choices were not delivered to the collector")
	}
	if strings.Contains(string(installTask.Config), `"interval_seconds"`) {
		t.Fatal("Center must not send a metrics interval override to the collector")
	}
	if err := store.CompleteTask(ctx, collector.ID, collector.Credential, installTask.ID, installTask.Attempt, true, "", mockPackageResult(t, installTask, json.RawMessage(`{"services":[]}`)), installTask.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	var runtime string
	if err := store.db.QueryRow(`SELECT runtime FROM applications WHERE id = ?`, installed.ApplicationID).Scan(&runtime); err != nil || runtime != "host" {
		t.Fatalf("collector runtime=%s err=%v", runtime, err)
	}
	plan, err := store.AgentReinstallPlan(ctx, collector.ID)
	if err != nil || len(plan.Applications) != 1 || plan.Applications[0].Recovery != "restore_data" || len(plan.Monitoring) != 0 {
		t.Fatalf("independent Agent recovery must preserve its data without remote credential operations: %+v %v", plan, err)
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: collector.ID, AppKey: senulunAgentAppKey, Operation: "configure", Config: json.RawMessage(`{"geoip_provider":"arbitrary"}`)}); err == nil {
		t.Fatal("invalid provider accepted by Center")
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: collector.ID, AppKey: senulunAgentAppKey, Operation: "configure", Config: json.RawMessage(`{"service_application_id":"unrelated-service"}`)}); err == nil {
		t.Fatal("administrator input forged a historical service association")
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: collector.ID, AppKey: senulunAgentAppKey, Operation: "configure", Config: json.RawMessage(`{"node_region":""}`)}); err != nil {
		t.Fatal(err)
	}
	changedTask := claimTask(t, store, collector)
	var changed senulun.AgentConfig
	if json.Unmarshal(changedTask.Config, &changed) != nil || changed.NodeRegion == nil || *changed.NodeRegion != "" || changed.GeoIPProvider == nil || *changed.GeoIPProvider != "disabled" || changed.ServiceURL != config.ServiceURL || changed.NodeName != config.NodeName {
		t.Fatal("configuration did not preserve identity, retain omitted options, and clear explicit empty region")
	}
	if strings.Contains(string(changedTask.Secrets), "test-test-test-test") {
		t.Fatal("configuration changes replayed the initial enrollment token")
	}
}

func TestSenulunAgentRequiresExplicitEnrollmentAndCreatesNoServiceCommand(t *testing.T) {
	store, service, collector, _ := senulunFixture(t)
	_, err := store.CreateDeployment(context.Background(), DeploymentRequest{AgentID: collector.ID, AppKey: senulunAgentAppKey, Config: json.RawMessage(`{"service_url":"https://independent.example.test/","node_name":"my-node","node_group":"servers"}`)})
	if err == nil {
		t.Fatal("initial installation accepted no enrollment token")
	}
	if task, err := store.ClaimNextTask(context.Background(), service.ID, service.Credential); err != nil || task != nil {
		t.Fatalf("installation created an unexpected monitoring-service command: %+v %v", task, err)
	}
}

func TestSenulunServicesAreIndependentPerHost(t *testing.T) {
	store, service, _, first := senulunFixture(t)
	other := enrollOrchestrationNode(t, store, "other-monitor", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.12", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.12", LANAddress: "10.0.0.12", EnabledKinds: []string{networking.KindLAN}})
	second, err := store.CreateDeployment(context.Background(), DeploymentRequest{AgentID: other.ID, AppKey: senulunAppKey, Config: json.RawMessage(`{"public_url":"https://other-monitor.example.test"}`)})
	if err != nil || first.ApplicationID == second.ApplicationID {
		t.Fatalf("a global monitoring-service singleton blocked an independent installation: %+v %v", second, err)
	}
	completeNextTask(t, store, other, "application.apply", json.RawMessage(`{"services":[{"name":"dashboard","protocol":"http","containerPort":8080,"hostPort":18080,"address":"10.0.0.12"}]}`))
	if _, err := store.CreateDeployment(context.Background(), DeploymentRequest{AgentID: service.ID, AppKey: senulunAppKey, Operation: "uninstall"}); err != nil {
		t.Fatalf("another independent service prevented uninstall: %v", err)
	}
}

func TestSenulunMigrationPreservesExistingCommands(t *testing.T) {
	dir := t.TempDir()
	legacy := legacyMigrationStore(t, dir, 70)
	if _, err := legacy.db.Exec(`INSERT INTO application_commands(rowid,id,application_id,agent_id,gateway_node_id,kind,input_json,state,attempt,created_at,updated_at) VALUES(81,'old-command','application-v3','agent-v3','agent-v3','3xui.reality.rename','{"action":"rename"}','pending',3,'created','updated')`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	store := openBeforeCatalogMaintenanceForTest(t, dir)
	defer store.Close()
	var rowid, attempt int
	var input, state string
	if err := store.db.QueryRow(`SELECT rowid,attempt,input_json,state FROM application_commands WHERE id = 'old-command'`).Scan(&rowid, &attempt, &input, &state); err != nil {
		t.Fatal(err)
	}
	if rowid != 81 || attempt != 3 || input != `{"action":"rename"}` || state != "pending" {
		t.Fatal("migration changed pending 3x-ui work")
	}
	for _, id := range []string{"pulse-one", "pulse-two"} {
		if _, err := store.db.Exec(`INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) VALUES(?,'application-v3','agent-v3','agent-v3','pulse.enrollment.create',?,'pending','','')`, id, `{"deploymentId":"`+id+`"}`); err != nil {
			t.Fatal(err)
		}
	}
	backups, err := filepath.Glob(filepath.Join(dir, "migration-backups", fmt.Sprintf("center-v70-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatal("pre-migration backup missing")
	}
	finishCatalogMaintenanceFixture(t, store, dir)
}
