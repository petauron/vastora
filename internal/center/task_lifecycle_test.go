package center

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestExpiredTaskIsRetriedAndStaleResultIsRejected(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	clock := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	node := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.10", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.10", LANAddress: "10.0.0.10", EnabledKinds: []string{networking.KindLAN}})
	config := json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Config: config}); err != nil {
		t.Fatal(err)
	}
	first := claimTask(t, store, node)
	clock = clock.Add(taskLeaseDuration + time.Second)
	second := claimTask(t, store, node)
	if first.ID != second.ID || first.Attempt != 1 || second.Attempt != 2 || first.Revision != second.Revision {
		t.Fatalf("unexpected retry claims: first=%#v second=%#v", first, second)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, first.ID, first.Attempt, false, "late result", nil); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale result was not rejected: %v", err)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, second.ID, second.Attempt, false, "expected failure", nil); err != nil {
		t.Fatal(err)
	}
	actions, err := store.ListActions(ctx, defaultActionLimit)
	if err != nil {
		t.Fatal(err)
	}
	events := map[string]int{}
	for _, action := range actions {
		if action.TaskID == first.ID {
			events[action.Event]++
		}
	}
	if events["queued"] != 1 || events["claimed"] != 2 || events["lease_expired"] != 1 || events["failed"] != 1 {
		t.Fatalf("unexpected task audit trail: %#v", events)
	}
	deployments, err := store.ListDeployments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(deployments) != 1 || deployments[0].State != "failed" || deployments[0].Error != "expected failure" || deployments[0].OneTimeCredentials != nil {
		t.Fatalf("failed operation is not safely visible to the UI: %#v", deployments)
	}
}

func TestDeploymentLifecyclePreventsDuplicateInstallAndControlsDataDeletion(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.20", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.20", LANAddress: "10.0.0.20", EnabledKinds: []string{networking.KindLAN}})
	config := json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Operation: "uninstall"}); err == nil {
		t.Fatal("uninstall was accepted before installation")
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Config: config}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Config: config}); err == nil || !strings.Contains(err.Error(), "active deployment task") {
		t.Fatalf("parallel install was not rejected: %v", err)
	}
	completeNextTask(t, store, node, "application.apply", json.RawMessage(`{"services":[{"name":"api","protocol":"http","containerPort":8317,"hostPort":8317,"address":"10.0.0.20"}]}`))
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Config: config}); err == nil || !strings.Contains(err.Error(), "use upgrade") {
		t.Fatalf("duplicate install was not rejected: %v", err)
	}
	uninstall, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Operation: "uninstall", DeleteData: false})
	if err != nil {
		t.Fatal(err)
	}
	if uninstall.DeleteData {
		t.Fatal("uninstall deleted data without explicit selection")
	}
	completeNextTask(t, store, node, "application.apply", nil)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Operation: "upgrade"}); err == nil {
		t.Fatal("upgrade was accepted after uninstall")
	}
}

func TestConfigureReusesInstalledVersionAndEncryptedValues(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.30", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.30", LANAddress: "10.0.0.30", EnabledKinds: []string{networking.KindLAN}})
	initial := json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Config: initial}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "application.apply", json.RawMessage(`{"services":[{"name":"api","protocol":"http","containerPort":8317,"hostPort":8317,"address":"10.0.0.30"}]}`))
	configured, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Operation: "configure", Config: json.RawMessage(`{"timezone":"Asia/Singapore","debug":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if configured.AppVersion != "7.2.128" {
		t.Fatalf("configure changed installed version: %#v", configured)
	}
	task := claimTask(t, store, node)
	var config map[string]any
	var secrets map[string]string
	if json.Unmarshal(task.Config, &config) != nil || json.Unmarshal(task.Secrets, &secrets) != nil {
		t.Fatalf("configure task returned invalid configuration: %#v", task)
	}
	if config["timezone"] != "Asia/Singapore" || config["debug"] != true || secrets["management_key"] != "management-secret" || secrets["api_key"] != "client-secret" {
		t.Fatalf("configure did not merge encrypted prior values: config=%#v secrets=%#v", config, secrets)
	}
}

func TestUpgradeRequiresANewerCatalogVersionAndRejectsDowngrade(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "versioned", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.31", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.31", LANAddress: "10.0.0.31", EnabledKinds: []string{networking.KindLAN}})
	initial := json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: initial}); err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"services":[{"name":"api","protocol":"http","containerPort":8317,"hostPort":8317,"address":"10.0.0.31"}]}`)
	completeNextTask(t, store, node, "application.apply", result)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Operation: "upgrade"}); err == nil || !strings.Contains(err.Error(), "already at version") {
		t.Fatalf("same-version upgrade was accepted: %v", err)
	}
	seedCatalogVersion(t, store, "7.2.129")
	upgrade, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Operation: "upgrade"})
	if err != nil || upgrade.AppVersion != "7.2.129" {
		t.Fatalf("newer version was not accepted: %#v err=%v", upgrade, err)
	}
	completeNextTask(t, store, node, "application.apply", result)
	seedCatalogVersion(t, store, "7.2.127")
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Operation: "upgrade"}); err == nil || !strings.Contains(err.Error(), "downgrade is not allowed") {
		t.Fatalf("downgrade was accepted: %v", err)
	}
}

func TestFailedChangeRemainsAnInstalledApplication(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "degraded", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.32", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.32", LANAddress: "10.0.0.32", EnabledKinds: []string{networking.KindLAN}})
	initial := json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: initial}); err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"services":[{"name":"api","protocol":"http","containerPort":8317,"hostPort":8317,"address":"10.0.0.32"}]}`)
	completeNextTask(t, store, node, "application.apply", result)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Operation: "configure", Config: json.RawMessage(`{"debug":true}`)}); err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, false, "replacement failed", nil); err != nil {
		t.Fatal(err)
	}
	applications, err := store.ListApplications(ctx)
	if err != nil || len(applications) != 1 || applications[0].InstalledVersion != "7.2.128" || applications[0].Status != "failed" {
		t.Fatalf("failed change lost installed state: %#v err=%v", applications, err)
	}
}

func TestInstalledAppCanBeUninstalledAfterCatalogRemoval(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "catalog-removed", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.33", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.33", LANAddress: "10.0.0.33", EnabledKinds: []string{networking.KindLAN}})
	initial := json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: initial}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "application.apply", json.RawMessage(`{"services":[{"name":"api","protocol":"http","containerPort":8317,"hostPort":8317,"address":"10.0.0.33"}]}`))
	if _, err := store.db.ExecContext(ctx, `UPDATE catalog_sources SET enabled = 0`); err != nil {
		t.Fatal(err)
	}
	uninstall, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Operation: "uninstall"})
	if err != nil || uninstall.AppVersion != "7.2.128" {
		t.Fatalf("installed app became unmanageable after catalog removal: %#v err=%v", uninstall, err)
	}
}

func seedCatalogVersion(t *testing.T, store *Store, version string) {
	t.Helper()
	payload, err := os.ReadFile("../../catalog/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	payload = bytes.Replace(payload, []byte(`"version": "7.2.128"`), []byte(`"version": "`+version+`"`), 1)
	if err := store.SeedOfficialCatalog(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
}

func TestTaskIDsAreScopedByAgentAndRevision(t *testing.T) {
	if gatewayRouteTaskID("agent-a", 1) == gatewayRouteTaskID("agent-b", 1) || gatewayComponentTaskID("agent-a", 1) == gatewayComponentTaskID("agent-b", 1) || tunnelTaskID("agent-a", 1) == tunnelTaskID("agent-b", 1) {
		t.Fatal("task IDs collide across agents")
	}
	if revision, ok := gatewayTaskRevision(gatewayRouteTaskID("agent-a", 7)); !ok || revision != 7 {
		t.Fatal("gateway task revision was not parsed")
	}
	if revision, ok := tunnelTaskRevision(tunnelTaskID("agent-a", 8)); !ok || revision != 8 {
		t.Fatal("Tunnel task revision was not parsed")
	}
}

func TestThreeXUICredentialsAreReturnedOnceAndRedactedFromLists(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.40", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.40", LANAddress: "10.0.0.40", EnabledKinds: []string{networking.KindLAN}})
	created, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/3x-ui", Role: threeXUIRoleMaster, Config: json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	if created.OneTimeCredentials == nil || created.OneTimeCredentials.Username == "" || len(created.OneTimeCredentials.Password) < 20 {
		t.Fatalf("3x-ui did not return strong one-time credentials: %#v", created.OneTimeCredentials)
	}
	task := claimTask(t, store, node)
	var secrets map[string]string
	if json.Unmarshal(task.Secrets, &secrets) != nil || secrets["username"] != created.OneTimeCredentials.Username || secrets["password"] != created.OneTimeCredentials.Password {
		t.Fatalf("Agent task did not receive matching encrypted credentials: %#v", secrets)
	}
	result := json.RawMessage(`{"services":[{"name":"panel","protocol":"http","containerPort":2053,"hostPort":2053,"address":"10.0.0.40"},{"name":"subscription","protocol":"http","containerPort":2096,"hostPort":2096,"address":"10.0.0.40"}],"generatedSecrets":{"api_token":"local-api-token"}}`)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", result); err != nil {
		t.Fatal(err)
	}
	listed, err := store.ListDeployments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(listed)
	if bytes.Contains(encoded, []byte(created.OneTimeCredentials.Password)) || bytes.Contains(encoded, []byte("local-api-token")) || listed[0].OneTimeCredentials != nil {
		t.Fatalf("deployment list leaked one-time credentials: %s", encoded)
	}
}

func TestIncompleteEndpointObservationPreservesLastSnapshot(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.41", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.41", LANAddress: "10.0.0.41", EnabledKinds: []string{networking.KindLAN}})
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleMaster, Config: json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`)}); err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	result := json.RawMessage(`{"services":[{"name":"panel","protocol":"http","containerPort":2053,"hostPort":2053,"address":"10.0.0.41"},{"name":"subscription","protocol":"http","containerPort":2096,"hostPort":2096,"address":"10.0.0.41"}],"generatedSecrets":{"api_token":"local-api-token"}}`)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", result); err != nil {
		t.Fatal(err)
	}
	heartbeat := NodeHeartbeat{Version: "test", Roles: []string{"worker"}, Capabilities: NodeCapabilities{Docker: true}, ApplicationEndpointsObserved: true, ApplicationEndpoints: []ApplicationEndpointObservation{{AppKey: threeXUIAppKey, Name: "inbound-7", Protocol: "tcp", AppProtocol: "vless/tcp", Listen: "0.0.0.0", Port: 443, Enabled: true}}}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	services, err := store.ListServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	observedStatus := ""
	for _, service := range services {
		if service.Name == "inbound-7" {
			observedStatus = service.Status
		}
	}
	if observedStatus != "ready" {
		t.Fatalf("initial endpoint observation was not stored: %#v", services)
	}
	heartbeat.ApplicationEndpoints = nil
	heartbeat.ApplicationEndpointsObserved = false
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	services, err = store.ListServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range services {
		if service.Name == "inbound-7" && service.Status != "ready" {
			t.Fatalf("incomplete observation stopped the last known endpoint: %#v", service)
		}
	}
	heartbeat.ApplicationEndpointsObserved = true
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	services, err = store.ListServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range services {
		if service.Name == "inbound-7" && service.Status != "stopped" {
			t.Fatalf("complete empty observation did not stop the disappeared endpoint: %#v", service)
		}
	}
}
