package center

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestExistingLegacyCrossSiteTopologyPreservesObservationsAndSecrets(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	originalSiteID := testSiteID(t, store)
	master := enrollOrchestrationNode(t, store, "subscription-controller", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.90", Interface: "eth0", Kind: networking.KindLAN}, {Address: "203.0.113.90", Interface: "eth0", Kind: networking.KindPublic}}, networking.Profile{ServiceAddress: "10.0.0.90", LANAddress: "10.0.0.90", PublicAddress: "203.0.113.90", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	worker := enrollOrchestrationNode(t, store, "vless-worker", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.91", Interface: "eth0", Kind: networking.KindLAN}, {Address: "203.0.113.91", Interface: "eth0", Kind: networking.KindPublic}}, networking.Profile{ServiceAddress: "10.0.0.91", LANAddress: "10.0.0.91", PublicAddress: "203.0.113.91", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	remoteSite, err := store.CreateSite(ctx, SiteInput{Name: "Remote", Code: "remote", Timezone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE agents SET site_id = ? WHERE id = ?`, remoteSite.ID, worker.ID); err != nil {
		t.Fatal(err)
	}
	masterDeployment := seedLegacyDeployment(t, store, master, "10.0.0.90", "master-api-token", threeXUIRoleMaster)
	workerDeployment := seedLegacyDeployment(t, store, worker, "10.0.0.91", "worker-api-token", threeXUIRoleWorker)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO three_x_ui_nodes(worker_application_id,master_application_id,remote_node_id,status,created_at,updated_at)
		VALUES(?,?,7,'ready','','')`, workerDeployment.ApplicationID, masterDeployment.ApplicationID); err != nil {
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
	if err := store.RecordAgentHeartbeat(ctx, master.ID, master.Credential, NodeHeartbeat{Version: "test", Roles: []string{"worker", "gateway"}, Capabilities: NodeCapabilities{Docker: true, Gateway: true}, GatewayHealthy: true, ApplicationEndpointsObserved: true, ApplicationEndpoints: []ApplicationEndpointObservation{{AppKey: threeXUIAppKey, Name: "inbound-12", Protocol: "tcp", AppProtocol: "vless/tcp/reality", Listen: "10.0.0.91", Port: 32123, Enabled: true, RemoteNodeID: 7, InboundTag: "advanced-reality-12", InboundTotalBytes: 1073741824}}}); err != nil {
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
	var observedPlan threeXUIInboundPlan
	if err := store.db.QueryRowContext(ctx, `SELECT service_id, inbound_tag, total_bytes, reset_day, next_reset_at, last_reset_at, revision, status, retry_at, attempt, last_error
		FROM three_x_ui_inbound_plans WHERE service_id = (SELECT id FROM services WHERE application_id = ? AND name = 'inbound-12')`, workerDeployment.ApplicationID).Scan(&observedPlan.ServiceID, &observedPlan.InboundTag, &observedPlan.TotalBytes, &observedPlan.ResetDay, &observedPlan.NextResetAt, &observedPlan.LastResetAt, &observedPlan.Revision, &observedPlan.Status, &observedPlan.RetryAt, &observedPlan.Attempt, &observedPlan.LastError); err != nil {
		t.Fatal(err)
	}
	if observedPlan.InboundTag != "advanced-reality-12" || observedPlan.TotalBytes != 1073741824 || observedPlan.ResetDay != 0 || observedPlan.Revision != 1 || observedPlan.Status != "active" {
		t.Fatalf("observed REALITY plan was not adopted: %#v", observedPlan)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE three_x_ui_inbound_plans SET total_bytes = 2147483648, reset_day = 15, revision = 2 WHERE service_id = ?`, observedPlan.ServiceID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAgentHeartbeat(ctx, master.ID, master.Credential, NodeHeartbeat{Version: "test", Roles: []string{"worker", "gateway"}, Capabilities: NodeCapabilities{Docker: true, Gateway: true}, GatewayHealthy: true, ApplicationEndpointsObserved: true, ApplicationEndpoints: []ApplicationEndpointObservation{{AppKey: threeXUIAppKey, Name: "inbound-12", Protocol: "tcp", AppProtocol: "vless/tcp/reality", Listen: "10.0.0.91", Port: 32123, Enabled: true, RemoteNodeID: 7, InboundTag: "advanced-reality-12", InboundTotalBytes: 536870912}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT total_bytes, reset_day, revision FROM three_x_ui_inbound_plans WHERE service_id = ?`, observedPlan.ServiceID).Scan(&observedPlan.TotalBytes, &observedPlan.ResetDay, &observedPlan.Revision); err != nil {
		t.Fatal(err)
	}
	if observedPlan.TotalBytes != 2147483648 || observedPlan.ResetDay != 15 || observedPlan.Revision != 2 {
		t.Fatalf("heartbeat overwrote a Center-managed REALITY plan: %#v", observedPlan)
	}
	if _, err := store.UpdateSite(ctx, originalSiteID, SiteInput{Name: "Test", Code: "test", Timezone: "UTC", GatewayNodes: []string{master.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateSite(ctx, remoteSite.ID, SiteInput{Name: "Remote", Code: "remote", Timezone: "UTC", GatewayNodes: []string{worker.ID}}); err != nil {
		t.Fatal(err)
	}
	// Retire the previously observed unmanaged inbound before asking Vastora to
	// create this host's single managed 443 node.
	if _, err := store.db.ExecContext(ctx, `UPDATE services SET status = 'stopped' WHERE application_id = ? AND app_protocol = 'vless/tcp/reality'`, workerDeployment.ApplicationID); err != nil {
		t.Fatal(err)
	}
	reality, err := createVerifiedRealityCommand(t, store, ctx, RealityCommandInput{ApplicationID: workerDeployment.ApplicationID, RegionCode: "US", Name: "Worker", ClientName: "Phone", Hostname: "reality.worker.example.test", DNSProvider: "manual", TargetHost: "www.example.com", ServerName: "www.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	var storedInput string
	if err := store.db.QueryRowContext(ctx, `SELECT CAST(input_json AS TEXT) FROM application_commands WHERE id = ?`, reality.ID).Scan(&storedInput); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(storedInput, "worker-api-token") {
		t.Fatal("worker API token was persisted in the REALITY command")
	}
	realityTask := claimTask(t, store, master)
	if realityTask.ApplicationCommand == nil || realityTask.ApplicationCommand.TargetApplicationID != workerDeployment.ApplicationID || realityTask.ApplicationCommand.TargetNodeID != 7 || realityTask.ApplicationCommand.TargetAddress != "10.0.0.91" || realityTask.ApplicationCommand.TargetAPIToken != "worker-api-token" || realityTask.ApplicationCommand.CreateInitialClient || realityTask.ApplicationCommand.ClientName != "" {
		t.Fatalf("unexpected cross-node REALITY task: %#v", realityTask)
	}
	if err := store.CompleteTask(ctx, master.ID, master.Credential, realityTask.ID, realityTask.Attempt, false, "simulated worker setup failure", nil, realityTask.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	if _, err := createVerifiedRealityCommand(t, store, ctx, RealityCommandInput{ApplicationID: masterDeployment.ApplicationID, RegionCode: "US", Name: "Controller", ClientName: "Phone", Hostname: "reality.controller.example.test", DNSProvider: "manual", TargetHost: "www.example.com", ServerName: "www.example.com"}); err != nil {
		t.Fatal(err)
	}
	controllerRealityTask := claimTask(t, store, master)
	if controllerRealityTask.ApplicationCommand == nil || !controllerRealityTask.ApplicationCommand.CreateInitialClient || controllerRealityTask.ApplicationCommand.ClientName != "Phone" {
		t.Fatalf("controller did not bootstrap its first subscription client after the worker REALITY service: %#v", controllerRealityTask)
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: master.ID, AppKey: threeXUIAppKey, Operation: "uninstall"}); err == nil || !strings.Contains(err.Error(), "VLESS nodes") {
		t.Fatalf("controller uninstall error = %v", err)
	}
}

func TestLegacyControllerObservationDoesNotOverwriteMigratedMeridianWorker(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	master := enrollOrchestrationNode(t, store, "legacy-master", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.90", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.90", LANAddress: "10.0.0.90", EnabledKinds: []string{networking.KindLAN}})
	worker := enrollOrchestrationNode(t, store, "migrated-worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.91", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.91", LANAddress: "10.0.0.91", EnabledKinds: []string{networking.KindLAN}})
	masterDeployment := seedLegacyDeployment(t, store, master, "10.0.0.90", "master-api-token", threeXUIRoleMaster)
	workerDeployment := seedLegacyDeployment(t, store, worker, "10.0.0.91", "worker-api-token", threeXUIRoleWorker)
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO three_x_ui_nodes(worker_application_id,master_application_id,remote_node_id,status,created_at,updated_at)
		VALUES(?,?,7,'ready',?,?)`, workerDeployment.ApplicationID, masterDeployment.ApplicationID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,app_protocol,status,created_at,updated_at)
		VALUES('migrated-service',?,?,'inbound-8','tcp',443,443,'10.0.0.91:443','observed',?,'ready',?,?)`, workerDeployment.ApplicationID, testSiteID(t, store), meridianEntryProtocol, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE applications SET app_key=?,role='' WHERE id=?`, meridianAppKey, workerDeployment.ApplicationID); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	observations := []ApplicationEndpointObservation{{AppKey: threeXUIAppKey, Name: "inbound-8", Protocol: "tcp", AppProtocol: "vless/tcp/reality", Listen: "10.0.0.91", Port: 443, Enabled: true, RemoteNodeID: 7, InboundTag: "legacy-worker-inbound"}}
	var cleanups []publicationCleanup
	if err := store.reconcileApplicationEndpoints(ctx, tx, master.ID, observations, store.now().UTC(), &cleanups); err != nil {
		t.Fatal(err)
	}
	var protocol string
	if err := tx.QueryRowContext(ctx, `SELECT app_protocol FROM services WHERE id='migrated-service'`).Scan(&protocol); err != nil {
		t.Fatal(err)
	}
	if protocol != meridianEntryProtocol {
		t.Fatalf("legacy controller overwrote migrated Meridian service protocol: %s", protocol)
	}
}
