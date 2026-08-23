package center

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/networking"
)

func TestThreeXUISiteControllerAndVLESSNodeLifecycle(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	master := enrollOrchestrationNode(t, store, "subscription-controller", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.90", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}, {Address: "203.0.113.90", Interface: "eth0", Family: "ipv4", Kind: networking.KindPublic}}, networking.Profile{ServiceAddress: "10.0.0.90", LANAddress: "10.0.0.90", PublicAddress: "203.0.113.90", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	worker := enrollOrchestrationNode(t, store, "vless-worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.91", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.91", LANAddress: "10.0.0.91", EnabledKinds: []string{networking.KindLAN}})
	config := json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`)

	masterDeployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: master.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleMaster, Config: config})
	if err != nil {
		t.Fatal(err)
	}
	if masterDeployment.OneTimeCredentials == nil {
		t.Fatal("Site controller did not return its one-time administrator credentials")
	}
	masterTask := claimTask(t, store, master)
	if masterTask.ApplicationRole != threeXUIRoleMaster {
		t.Fatalf("controller role = %q", masterTask.ApplicationRole)
	}
	completeThreeXUIDeployment(t, store, master, masterTask, "10.0.0.90", "master-api-token")

	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: worker.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleMaster, Config: config}); err == nil || !strings.Contains(err.Error(), "already has") {
		t.Fatalf("second Site controller error = %v", err)
	}
	workerDeployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: worker.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleWorker, Config: config})
	if err != nil {
		t.Fatal(err)
	}
	if workerDeployment.OneTimeCredentials != nil {
		t.Fatal("VLESS worker exposed an unused administrator account")
	}
	workerTask := claimTask(t, store, worker)
	if workerTask.ApplicationRole != threeXUIRoleWorker {
		t.Fatalf("worker role = %q", workerTask.ApplicationRole)
	}
	completeThreeXUIDeployment(t, store, worker, workerTask, "10.0.0.91", "worker-api-token")

	var storedInput string
	if err := store.db.QueryRowContext(ctx, `SELECT CAST(input_json AS TEXT) FROM application_commands WHERE application_id = ? AND kind = ?`, workerDeployment.ApplicationID, nodeCommandKind).Scan(&storedInput); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(storedInput, "worker-api-token") {
		t.Fatal("worker API token was persisted in the command payload")
	}
	nodeTask := claimTask(t, store, master)
	if nodeTask.NodeCommand == nil || nodeTask.NodeCommand.Action != "reconcile" || nodeTask.NodeCommand.Address != "10.0.0.91" || nodeTask.NodeCommand.APIToken != "worker-api-token" {
		t.Fatalf("unexpected controller node task: %#v", nodeTask)
	}
	result, _ := json.Marshal(ApplicationTaskResult{NodeCommand: &ThreeXUINodeCommandResult{RemoteNodeID: 7, Status: "ready"}})
	if err := store.CompleteTask(ctx, master.ID, master.Credential, nodeTask.ID, nodeTask.Attempt, true, "", result); err != nil {
		t.Fatal(err)
	}

	applications, err := store.ListApplications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var listedWorker ApplicationView
	for _, application := range applications {
		if application.ID == workerDeployment.ApplicationID {
			listedWorker = application
		}
	}
	if listedWorker.Role != threeXUIRoleWorker || listedWorker.ControllerID != masterDeployment.ApplicationID || listedWorker.NodeSyncStatus != "ready" {
		t.Fatalf("unexpected VLESS worker topology: %#v", listedWorker)
	}
	if err := store.RecordAgentHeartbeat(ctx, master.ID, master.Credential, NodeHeartbeat{Version: "test", Roles: []string{"worker", "gateway"}, Capabilities: NodeCapabilities{Docker: true, Gateway: true}, GatewayHealthy: true, ApplicationEndpointsObserved: true, ApplicationEndpoints: []ApplicationEndpointObservation{{AppKey: threeXUIAppKey, Name: "inbound-12", Protocol: "tcp", AppProtocol: "vless/tcp/reality", Listen: "10.0.0.91", Port: 32123, Enabled: true, RemoteNodeID: 7}}}); err != nil {
		t.Fatal(err)
	}
	services, err := store.ListServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	workerInboundFound := false
	for _, service := range services {
		if service.ApplicationID == workerDeployment.ApplicationID && service.Name == "inbound-12" && service.Endpoint == "10.0.0.91:32123" {
			workerInboundFound = true
		}
	}
	if !workerInboundFound {
		t.Fatalf("controller observation was not assigned to its worker: %#v", services)
	}
	reality, err := store.CreateRealityCommand(ctx, RealityCommandInput{ApplicationID: workerDeployment.ApplicationID, Name: "Phone", GatewayNodeID: master.ID, Hostname: "reality.worker.example.test", DNSProvider: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT CAST(input_json AS TEXT) FROM application_commands WHERE id = ?`, reality.ID).Scan(&storedInput); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(storedInput, "worker-api-token") {
		t.Fatal("worker API token was persisted in the REALITY command")
	}
	realityTask := claimTask(t, store, master)
	if realityTask.ApplicationCommand == nil || realityTask.ApplicationCommand.TargetApplicationID != workerDeployment.ApplicationID || realityTask.ApplicationCommand.TargetNodeID != 7 || realityTask.ApplicationCommand.TargetAddress != "10.0.0.91" || realityTask.ApplicationCommand.TargetAPIToken != "worker-api-token" {
		t.Fatalf("unexpected cross-node REALITY task: %#v", realityTask)
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: master.ID, AppKey: threeXUIAppKey, Operation: "uninstall"}); err == nil || !strings.Contains(err.Error(), "VLESS nodes") {
		t.Fatalf("controller uninstall error = %v", err)
	}
}

func completeThreeXUIDeployment(t *testing.T, store *Store, node AgentCredential, task *AgentTask, address, apiToken string) {
	t.Helper()
	result, _ := json.Marshal(ApplicationTaskResult{
		Services: []ApplicationServiceResult{
			{Name: "panel", Protocol: "http", ContainerPort: 2053, HostPort: 2053, Address: address},
			{Name: "subscription", Protocol: "http", ContainerPort: 2096, HostPort: 2096, Address: address},
		},
		GeneratedSecrets: map[string]string{"api_token": apiToken},
	})
	if err := store.CompleteTask(context.Background(), node.ID, node.Credential, task.ID, task.Attempt, true, "", result); err != nil {
		t.Fatal(err)
	}
}
