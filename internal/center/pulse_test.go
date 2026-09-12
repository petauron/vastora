package center

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/pulse"
)

func pulseFixture(t *testing.T) (*Store, AgentCredential, AgentCredential, DeploymentView) {
	t.Helper()
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	service := enrollOrchestrationNode(t, store, "monitor", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.10", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.10", LANAddress: "10.0.0.10", EnabledKinds: []string{networking.KindLAN}})
	collector := enrollOrchestrationNode(t, store, "collector", NodeCapabilities{}, []networking.Candidate{{Address: "10.0.0.11", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.11", LANAddress: "10.0.0.11", EnabledKinds: []string{networking.KindLAN}})
	deployment, err := store.CreateDeployment(context.Background(), DeploymentRequest{AgentID: service.ID, AppKey: pulseAppKey, Config: json.RawMessage(`{"public_url":"https://pulse.example.com","setup_token":"test-only-pulse-setup-token-0000000000"}`)})
	if err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, service, "application.apply", json.RawMessage(`{"services":[{"name":"dashboard","protocol":"http","containerPort":8080,"hostPort":18080,"address":"10.0.0.10"}]}`))
	return store, service, collector, deployment
}

func addPulsePrivateFixture(t *testing.T, store *Store, applicationID, nodeID string) {
	t.Helper()
	_, err := store.db.Exec(`INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,dns_provider,tls_enabled,status,created_at,updated_at)
		SELECT 'pulse-private',id,'lan_gateway','site_gateway',?,'pulse.private.example.com','manual',1,'ready','','' FROM services WHERE application_id = ? AND name = 'dashboard'`, nodeID, applicationID)
	if err != nil {
		t.Fatal(err)
	}
}

func TestPulseEnrollmentPrivateHTTPSAndNativeInstall(t *testing.T) {
	store, service, collector, deployment := pulseFixture(t)
	ctx := context.Background()
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: collector.ID, AppKey: pulseAppKey}); err == nil {
		t.Fatal("second global monitoring service accepted")
	}
	request := DeploymentRequest{AgentID: collector.ID, AppKey: pulseAgentAppKey, Config: json.RawMessage(`{"node_region":"SG","geoip_provider":"disabled","interval_seconds":3}`)}
	if _, err := store.CreateDeployment(ctx, request); err == nil {
		t.Fatal("collector accepted without private HTTPS")
	}
	addPulsePrivateFixture(t, store, deployment.ApplicationID, service.ID)
	installed, err := store.CreateDeployment(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if task, err := store.ClaimNextTask(ctx, collector.ID, collector.Credential); err != nil || task != nil {
		t.Fatalf("installation claimed before enrollment: %+v %v", task, err)
	}
	task := claimTask(t, store, service)
	if task.PulseEnrollment == nil || task.PulseEnrollment.DeploymentID != installed.ID {
		t.Fatal("wrong enrollment recipient")
	}
	token := strings.Repeat("e", 32)
	raw, _ := json.Marshal(map[string]any{"pulseEnrollment": pulse.EnrollmentResult{ID: "enrollment-id", Token: token, ExpiresAtUnixMS: store.now().Add(time.Hour).UnixMilli()}})
	if err := store.CompleteTask(ctx, service.ID, service.Credential, task.ID, task.Attempt, true, "", raw, 0); err != nil {
		t.Fatal(err)
	}
	var resultJSON []byte
	if err := store.db.QueryRow(`SELECT result_json FROM application_commands WHERE id = ?`, task.ID).Scan(&resultJSON); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(resultJSON), token) {
		t.Fatal("enrollment token leaked into command result")
	}
	installTask := claimTask(t, store, collector)
	if installTask.AppKey != pulseAgentAppKey || !strings.Contains(string(installTask.Secrets), token) {
		t.Fatal("enrollment secret not delivered to the collector")
	}
	var config pulse.AgentConfig
	if json.Unmarshal(installTask.Config, &config) != nil || config.NodeName != "collector" || config.ServiceApplicationID != deployment.ApplicationID {
		t.Fatal("collector identity was not inherited")
	}
	if config.NodeRegion == nil || *config.NodeRegion != "SG" || config.GeoIPProvider == nil || *config.GeoIPProvider != "disabled" || config.IntervalSeconds != 3 {
		t.Fatal("administrator location and interval choices were not delivered to the collector")
	}
	if err := store.CompleteTask(ctx, collector.ID, collector.Credential, installTask.ID, installTask.Attempt, true, "", json.RawMessage(`{"services":[]}`), installTask.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	var runtime string
	if err := store.db.QueryRow(`SELECT runtime FROM applications WHERE id = ?`, installed.ApplicationID).Scan(&runtime); err != nil || runtime != "host" {
		t.Fatalf("collector runtime=%s err=%v", runtime, err)
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: service.ID, AppKey: pulseAppKey, Operation: "uninstall"}); err == nil {
		t.Fatal("monitor removed while collectors still installed")
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: collector.ID, AppKey: pulseAgentAppKey, Operation: "configure", Config: json.RawMessage(`{"geoip_provider":"arbitrary"}`)}); err == nil {
		t.Fatal("invalid provider accepted by Center")
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: collector.ID, AppKey: pulseAgentAppKey, Operation: "configure", Config: json.RawMessage(`{"service_url":"https://attacker.example.com/"}`)}); err == nil {
		t.Fatal("administrator form was allowed to replace the managed service identity")
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: collector.ID, AppKey: pulseAgentAppKey, Operation: "configure", Config: json.RawMessage(`{"node_region":"","interval_seconds":10}`)}); err != nil {
		t.Fatal(err)
	}
	changedTask := claimTask(t, store, collector)
	var changed pulse.AgentConfig
	if json.Unmarshal(changedTask.Config, &changed) != nil || changed.NodeRegion == nil || *changed.NodeRegion != "" || changed.GeoIPProvider == nil || *changed.GeoIPProvider != "disabled" || changed.IntervalSeconds != 10 || changed.ServiceURL != config.ServiceURL || changed.NodeName != config.NodeName {
		t.Fatal("configuration did not preserve identity, retain omitted options, and clear explicit empty region")
	}
}

func TestPulseEnrollmentFailureEndsPendingInstallation(t *testing.T) {
	store, service, collector, deployment := pulseFixture(t)
	addPulsePrivateFixture(t, store, deployment.ApplicationID, service.ID)
	installed, err := store.CreateDeployment(context.Background(), DeploymentRequest{AgentID: collector.ID, AppKey: pulseAgentAppKey})
	if err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, service)
	if err := store.CompleteTask(context.Background(), service.ID, service.Credential, task.ID, task.Attempt, false, "private upstream error", nil, 0); err != nil {
		t.Fatal(err)
	}
	var state, message string
	if err := store.db.QueryRow(`SELECT state,error FROM deployments WHERE id = ?`, installed.ID).Scan(&state, &message); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || strings.Contains(message, "private upstream error") {
		t.Fatalf("wrong failure state: %s %s", state, message)
	}
}

func TestPulseMigrationPreservesExistingCommands(t *testing.T) {
	dir := t.TempDir()
	legacy := legacyMigrationStore(t, dir, 70)
	if _, err := legacy.db.Exec(`INSERT INTO application_commands(rowid,id,application_id,agent_id,gateway_node_id,kind,input_json,state,attempt,created_at,updated_at) VALUES(81,'old-command','application-v3','agent-v3','agent-v3','3xui.reality.rename','{"action":"rename"}','pending',3,'created','updated')`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
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
}
