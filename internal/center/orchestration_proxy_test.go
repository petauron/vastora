package center

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
)

func TestSubscriptionCommandPublishesOnlyTheSubscriptionService(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "edge", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{
		{Address: "10.0.0.62", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "203.0.113.62", Interface: "eth0", Kind: networking.KindPublic},
	}, networking.Profile{ServiceAddress: "10.0.0.62", LANAddress: "10.0.0.62", PublicAddress: "203.0.113.62", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at) VALUES('three-x-ui-subscription', '3x-ui', ?, ?, ?, '', 'running', 'docker', 'master', ?, ?)`, node.ID, testSiteID(t, store), threeXUIAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	selectTestThreeXUIController(t, store, "three-x-ui-subscription")
	for _, service := range []struct {
		id, name string
		port     int
		manager  int
	}{{"three-x-ui-panel", "panel", 2053, 1}, {"three-x-ui-subscription", "subscription", 2096, 0}} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, management, status, created_at, updated_at) VALUES(?, 'three-x-ui-subscription', ?, ?, 'http', ?, ?, ?, 'catalog', ?, 'ready', ?, ?)`, service.id, testSiteID(t, store), service.name, service.port, service.port, net.JoinHostPort("10.0.0.62", fmt.Sprint(service.port)), service.manager, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE sites SET domain_suffix = 'example.test' WHERE id = ?`, testSiteID(t, store)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePublication(ctx, PublicationInput{ServiceID: "three-x-ui-subscription", Kind: publicationPublic, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID}, Hostname: "wrong.example.test", DNSProvider: "manual"}); err == nil || !strings.Contains(err.Error(), "public subscription workflow") {
		t.Fatalf("generic subscription publication error = %v", err)
	}
	command, err := store.CreateSubscriptionCommand(ctx, SubscriptionCommandInput{ApplicationID: "three-x-ui-subscription", GatewayNodeID: node.ID, Kind: publicationPublic, DNSProvider: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if task.Kind != "application.command" || task.SubscriptionCommand == nil || task.ApplicationCommand != nil {
		t.Fatalf("unexpected subscription task: %#v", task)
	}
	label := strings.TrimSuffix(task.SubscriptionCommand.Domain, ".example.test")
	if len(label) != 26 || strings.Trim(label, "abcdefghijklmnopqrstuvwxyz234567") != "" || task.SubscriptionCommand.BaseURI != "https://"+task.SubscriptionCommand.Domain+"/sub/" {
		t.Fatalf("unexpected subscription settings: %#v", task.SubscriptionCommand)
	}
	result, _ := json.Marshal(ApplicationTaskResult{SubscriptionCommand: &SubscriptionCommandResult{Domain: task.SubscriptionCommand.Domain, BaseURI: task.SubscriptionCommand.BaseURI}})
	session := "subscription-confirmation-original-session"
	if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	auth, err := store.PersistExecutionAuthorization(ctx, node.ID, session, *task)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StartExecution(ctx, node.ID, session, auth.ID, auth.Digest); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreExecutionResult(ctx, node.ID, session, auth.ID, result, true, false, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE task_executions SET state='unknown' WHERE id=?`, auth.ID); err != nil {
		t.Fatal(err)
	}
	cookie, _, err := store.CreateFirstAdmin(ctx, "subscription-confirmation-admin", "test-only-strong-password")
	if err != nil {
		t.Fatal(err)
	}
	adminID, err := store.SessionAdminID(ctx, cookie)
	if err != nil {
		t.Fatal(err)
	}
	decision := controlplane.ExecutionDisposition{Action: "confirm-completed", ExecutionStopped: true, Note: "Verified stopped executor and subscription configuration."}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_subscription_confirmation BEFORE UPDATE ON task_executions WHEN NEW.disposition='confirm-completed' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmExecution(ctx, auth.ID, adminID, decision); err == nil {
		t.Fatal("confirmation ignored failed audit")
	}
	var failedProjectionState, failedProjectionResult, failedDisposition string
	if err := store.db.QueryRow(`SELECT state,result_json FROM application_commands WHERE id=?`, task.ID).Scan(&failedProjectionState, &failedProjectionResult); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT disposition FROM task_executions WHERE id=?`, auth.ID).Scan(&failedDisposition); err != nil {
		t.Fatal(err)
	}
	if failedProjectionState != "running" || failedProjectionResult != "{}" || failedDisposition != "" {
		t.Fatal("failed confirmation left partial command projection")
	}
	if _, err := store.db.Exec(`DROP TRIGGER reject_subscription_confirmation`); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfirmExecution(ctx, auth.ID, adminID, decision); err != nil {
		t.Fatal(err)
	}
	var historyState, disposition string
	if err := store.db.QueryRow(`SELECT state,disposition FROM task_executions WHERE id=?`, auth.ID).Scan(&historyState, &disposition); err != nil {
		t.Fatal(err)
	}
	if historyState != "unknown" || disposition != "confirm-completed" {
		t.Fatal("manual confirmation rewrote historical execution")
	}
	completed, err := store.ApplicationCommand(ctx, command.ID)
	if err != nil || completed.State != "succeeded" || completed.PublicationID == "" {
		t.Fatalf("unexpected completed subscription command: %#v err=%v", completed, err)
	}
	publication, err := store.Publication(ctx, completed.PublicationID)
	if err != nil || publication.ServiceID != "three-x-ui-subscription" || publication.TLSEnabled != true {
		t.Fatalf("subscription publication = %#v, err=%v", publication, err)
	}
	resyncInput := SubscriptionCommandInput{ApplicationID: "three-x-ui-subscription", GatewayNodeID: node.ID, Hostname: publication.Hostname, Kind: publicationPublic, DNSProvider: "manual"}
	resync, err := store.CreateSubscriptionCommand(ctx, resyncInput)
	if err != nil || resync.PublicationID != publication.ID {
		t.Fatalf("subscription resync = %#v, err=%v", resync, err)
	}
	// A failed configuration task must not revoke a previously working entry.
	snapshot := func() string {
		var value string
		if err := store.db.QueryRow(`SELECT json_object('status',status,'desired',desired_revision,'applied',applied_revision,'updated',updated_at) FROM publications WHERE id=?`, publication.ID).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	failedTask := claimTask(t, store, node)
	if failedTask.ID != resync.ID {
		t.Fatalf("wrong resync task: %s", failedTask.Kind)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, failedTask.ID, failedTask.Attempt, false, "subscription API failed", nil, failedTask.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(); after != before {
		t.Fatalf("failed task changed publication: %s -> %s", before, after)
	}
	failed, err := store.ApplicationCommand(ctx, resync.ID)
	if err != nil || failed.State != "failed" {
		t.Fatalf("failure not recorded: %s %v", failed.State, err)
	}
	// Failure while recording another explicit command must likewise not undo
	// the reused publication. No compensation is authorized by a database error.
	if _, err := store.db.Exec(`CREATE TRIGGER reject_subscription_insert BEFORE INSERT ON application_commands WHEN NEW.kind='3xui.subscription.configure' BEGIN SELECT RAISE(ABORT,'command storage unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSubscriptionCommand(ctx, resyncInput); err == nil {
		t.Fatal("injected command failure was ignored")
	}
	if after := snapshot(); after != before {
		t.Fatalf("failed command creation changed publication: %s -> %s", before, after)
	}
	var subscriptionPublications int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE service_id = 'three-x-ui-subscription' AND status <> 'stopped'`).Scan(&subscriptionPublications); err != nil {
		t.Fatal(err)
	}
	if subscriptionPublications != 1 {
		t.Fatalf("active subscription publications = %d", subscriptionPublications)
	}
	var panelPublications int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE service_id = 'three-x-ui-panel' AND status <> 'stopped'`).Scan(&panelPublications); err != nil {
		t.Fatal(err)
	}
	if panelPublications != 0 {
		t.Fatal("3x-ui management panel was published with the subscription service")
	}
}

func TestMeridianSubscriptionIsServedByCenterWithoutAgentCommand(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "center-edge", NodeCapabilities{Docker: true, Gateway: true, Tunnel: true}, []networking.Candidate{
		{Address: "10.0.0.64", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "203.0.113.64", Interface: "eth0", Kind: networking.KindPublic},
	}, networking.Profile{ServiceAddress: "10.0.0.64", LANAddress: "10.0.0.64", PublicAddress: "203.0.113.64", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?)`, setupGatewayBindingSetting, `{"publicAddress":"203.0.113.64","bindAddress":"10.0.0.64"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES('meridian-center','Meridian',?,?,?,'','running','docker','',?,?)`, node.ID, testSiteID(t, store), meridianAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	serviceID, err := store.ensureMeridianSubscriptionService(ctx, "meridian-center")
	if err != nil || serviceID == "" {
		t.Fatalf("Meridian subscription service = %q, err=%v", serviceID, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE sites SET domain_suffix='example.test' WHERE id=?`, testSiteID(t, store)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePublication(ctx, PublicationInput{ServiceID: serviceID, Kind: publicationPublic, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID}, Hostname: "wrong.example.test", DNSProvider: "manual"}); err == nil || !strings.Contains(err.Error(), "dedicated public subscription workflow") {
		t.Fatalf("generic Meridian publication error = %v", err)
	}
	command, err := store.CreateSubscriptionCommand(ctx, SubscriptionCommandInput{ApplicationID: "meridian-center", GatewayNodeID: node.ID, Kind: publicationPublic, DNSProvider: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if command.Kind != meridianSubscriptionCommandKind || command.State != "succeeded" || command.PublicationID == "" {
		t.Fatalf("Meridian subscription command = %#v", command)
	}
	var pending int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE id=? AND state IN ('pending','running')`, command.ID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatal("Meridian subscription publication queued an Agent command")
	}
}

func TestMeridianSubscriptionHostMovesOnlyAfterPublicationStops(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	first := enrollOrchestrationNode(t, store, "center-edge-a", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{
		{Address: "10.0.0.71", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "203.0.113.71", Interface: "eth0", Kind: networking.KindPublic},
	}, networking.Profile{ServiceAddress: "10.0.0.71", LANAddress: "10.0.0.71", PublicAddress: "203.0.113.71", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	second := enrollOrchestrationNode(t, store, "center-edge-b", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{
		{Address: "10.0.0.72", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "203.0.113.72", Interface: "eth0", Kind: networking.KindPublic},
	}, networking.Profile{ServiceAddress: "10.0.0.72", LANAddress: "10.0.0.72", PublicAddress: "203.0.113.72", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	completeNextTask(t, store, first, "gateway.component.apply", nil)
	completeNextTask(t, store, second, "gateway.component.apply", nil)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	siteID := testSiteID(t, store)
	for _, application := range []struct {
		id, nodeID string
	}{{"meridian-center-a", first.ID}, {"meridian-center-b", second.ID}} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
			VALUES(?,'Meridian',?,?,?,'','running','docker','',?,?)`, application.id, application.nodeID, siteID, meridianAppKey, now, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?)`, setupGatewayBindingSetting, `{"publicAddress":"203.0.113.71","bindAddress":"10.0.0.71"}`); err != nil {
		t.Fatal(err)
	}
	firstServiceID, err := store.ensureMeridianSubscriptionService(ctx, "meridian-center-a")
	if err != nil || firstServiceID == "" {
		t.Fatalf("first Meridian subscription service = %q, err=%v", firstServiceID, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE sites SET domain_suffix='example.test' WHERE id=?`, siteID); err != nil {
		t.Fatal(err)
	}
	command, err := store.CreateSubscriptionCommand(ctx, SubscriptionCommandInput{ApplicationID: "meridian-center-a", GatewayNodeID: first.ID, Kind: publicationPublic, DNSProvider: "manual"})
	if err != nil || command.PublicationID == "" {
		t.Fatalf("create first Meridian subscription publication: %#v err=%v", command, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE settings SET value=? WHERE key=?`, `{"publicAddress":"203.0.113.72","bindAddress":"10.0.0.72"}`, setupGatewayBindingSetting); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ensureMeridianSubscriptionService(ctx, "meridian-center-b"); err == nil || !strings.Contains(err.Error(), "remove the active Meridian subscription entry") {
		t.Fatalf("unsafe Meridian subscription host move error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE publications SET status='stopped' WHERE id=?`, command.PublicationID); err != nil {
		t.Fatal(err)
	}
	secondServiceID, err := store.ensureMeridianSubscriptionService(ctx, "meridian-center-b")
	if err != nil || secondServiceID == "" || secondServiceID == firstServiceID {
		t.Fatalf("moved Meridian subscription service = %q, err=%v", secondServiceID, err)
	}
	var firstStatus, secondStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM services WHERE id=?`, firstServiceID).Scan(&firstStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM services WHERE id=?`, secondServiceID).Scan(&secondStatus); err != nil {
		t.Fatal(err)
	}
	if firstStatus != "stopped" || secondStatus != "ready" {
		t.Fatalf("subscription service statuses = old %q, new %q", firstStatus, secondStatus)
	}
}

func TestGatewayCertificatePrivateKeyIsAbsentFromDesiredStateAndActions(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "private-gateway", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.63", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.63", LANAddress: "10.0.0.63", EnabledKinds: []string{networking.KindLAN}})
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{Name: "Test", Code: "test", Timezone: "UTC", DomainSuffix: "example.test", GatewayNodes: []string{node.ID}}); err != nil {
		t.Fatal(err)
	}
	configureCloudflareZoneForTest(t, store, "example.test")
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	applicationID := installCPA(t, store, node, "10.0.0.63")
	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 2 || services[0].ApplicationID != applicationID {
		t.Fatalf("services = %#v, err=%v", services, err)
	}
	publication, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationLAN, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID}, Hostname: "cpa.test.example.test", DNSProvider: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	var certificate managedCertificate
	store.issuePrivateCertificate = func(_ context.Context, dnsNames ...string) (managedCertificate, error) {
		certificate = testManagedCertificate(t, dnsNames...)
		return certificate, nil
	}
	if _, err := store.UpdatePublicationTLS(ctx, publication.ID, true); err != nil {
		t.Fatal(err)
	}
	var desiredJSON []byte
	if err := store.db.QueryRowContext(ctx, `SELECT desired_json FROM gateway_states WHERE gateway_node_id = ?`, node.ID).Scan(&desiredJSON); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(desiredJSON), "PRIVATE KEY") {
		t.Fatal("gateway desired state contains a certificate private key")
	}
	actions, err := store.ListActions(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	actionJSON, _ := json.Marshal(actions)
	if strings.Contains(string(actionJSON), "PRIVATE KEY") {
		t.Fatal("task events contain a certificate private key")
	}
	task := claimTask(t, store, node)
	if task.Kind != "gateway.routes.apply" || len(task.GatewayCertificates) != 1 || task.GatewayCertificates[0].PrivateKeyPEM != certificate.PrivateKeyPEM {
		t.Fatalf("certificate was not delivered only with the Agent task: %#v", task)
	}
}

func TestRealityNodeCanBeRenamedWithoutChangingServiceIdentity(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "edge", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.71", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.71", LANAddress: "10.0.0.71", EnabledKinds: []string{networking.KindLAN}})
	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleMaster, Config: json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	installTask := claimTask(t, store, node)
	completeThreeXUIDeployment(t, store, node, installTask, "10.0.0.71", "edge-api-token")
	now := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, display_name, protocol, container_port, host_port, endpoint, source, app_protocol, management, observed_listen, status, created_at, updated_at)
		VALUES('reality-service', ?, ?, 'inbound-9', 'Old name', 'tcp', 32009, 32009, '10.0.0.71:32009', 'observed', 'vless/tcp/reality', 0, '10.0.0.71', 'ready', ?, ?)`, deployment.ApplicationID, testSiteID(t, store), now, now); err != nil {
		t.Fatal(err)
	}
	command, err := store.CreateRealityRenameCommand(ctx, RealityRenameCommandInput{ServiceID: "reality-service", RegionCode: "US", Name: "Provider A"})
	if err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if task.ApplicationCommand == nil || task.ApplicationCommand.Action != "rename" || task.ApplicationCommand.InboundID != 9 || task.ApplicationCommand.RegionCode != "US" || task.ApplicationCommand.DisplayName != "🇺🇸 美国｜Provider A" {
		t.Fatalf("unexpected rename task: %#v", task)
	}
	encoded, _ := json.Marshal(ApplicationTaskResult{ApplicationCommand: &RealityCommandResult{Action: "rename", InboundID: 9, DisplayName: "🇺🇸 美国｜Provider A"}})
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", encoded, task.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	completed, err := store.ApplicationCommand(ctx, command.ID)
	if err != nil || completed.State != "succeeded" || completed.RegionCode != "US" || completed.DisplayName != "🇺🇸 美国｜Provider A" {
		t.Fatalf("unexpected completed rename: %#v err=%v", completed, err)
	}
	var displayName, region, serviceName, endpoint string
	if err := store.db.QueryRowContext(ctx, `SELECT display_name, region_code, name, endpoint FROM services WHERE id = 'reality-service'`).Scan(&displayName, &region, &serviceName, &endpoint); err != nil {
		t.Fatal(err)
	}
	if displayName != "🇺🇸 美国｜Provider A" || region != "US" || serviceName != "inbound-9" || endpoint != "10.0.0.71:32009" {
		t.Fatalf("renamed service = display %q, region %q, identity %q, endpoint %q", displayName, region, serviceName, endpoint)
	}
}

func TestValidateRealityCommandResultRejectsTamperedClientLink(t *testing.T) {
	input := RealityCommandTask{Action: "create", RegionCode: "US", DisplayName: "🇺🇸 美国｜Edge", ClientName: "MacBook", ConnectHostname: "reality.edge.site.example.test", TargetHost: "www.example.com", ServerName: "www.example.com", TargetAddress: "10.0.0.61", InboundTag: "vastora-test", CreateInitialClient: true}
	valid := RealityCommandResult{
		Action:           "create",
		InboundID:        9,
		DisplayName:      "🇺🇸 美国｜Edge",
		ClientName:       "MacBook",
		Listen:           "10.0.0.61",
		Port:             443,
		TargetHost:       "www.example.com",
		TargetIP:         "203.0.113.10",
		ServerName:       "www.example.com",
		NodeASN:          64500,
		TargetASN:        64496,
		TLS13:            true,
		X25519:           true,
		HTTP2:            true,
		CertificateValid: true,
		GuardStatus:      "ready",
		ProxyProtocol:    true,
		ConnectHostname:  "reality.edge.site.example.test",
		ShareURI:         "vless://f47ac10b-58cc-4372-a567-0e02b2c3d479@reality.edge.site.example.test:443?type=tcp&security=reality&flow=xtls-rprx-vision&sni=www.example.com&pbk=public-key&sid=0123456789abcdef#%F0%9F%87%BA%F0%9F%87%B8%20%E7%BE%8E%E5%9B%BD%EF%BD%9CEdge",
		InboundTag:       "vastora-test",
		ClientCreated:    true,
	}
	proofJSON, _ := json.Marshal(valid)
	if err := json.Unmarshal(proofJSON, &input.VerifiedTarget); err != nil {
		t.Fatal(err)
	}
	if err := validateRealityCommandResult(input, valid); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	unknownASN := valid
	unknownASN.NodeASN, unknownASN.TargetASN = 0, 0
	if err := validateRealityCommandResult(input, unknownASN); err != nil {
		t.Fatalf("unknown advisory ASN rejected a valid result: %v", err)
	}
	for name, mutate := range map[string]func(*RealityCommandResult){
		"changed verified IP": func(value *RealityCommandResult) { value.TargetIP = "203.0.113.11" },
		"private service address": func(value *RealityCommandResult) {
			value.Listen = "10.0.0.62"
		},
		"connection hostname": func(value *RealityCommandResult) {
			value.ShareURI = strings.Replace(value.ShareURI, "reality.edge.site.example.test", "attacker.example.test", 1)
		},
		"camouflage SNI": func(value *RealityCommandResult) {
			value.ShareURI = strings.Replace(value.ShareURI, "sni=www.example.com", "sni=attacker.example.test", 1)
		},
		"missing public key": func(value *RealityCommandResult) {
			value.ShareURI = strings.Replace(value.ShareURI, "pbk=public-key", "pbk=", 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := validateRealityCommandResult(input, candidate); err == nil {
				t.Fatal("tampered result was accepted")
			}
		})
	}
}

func TestRealityTargetHostnameRequiresDotCom(t *testing.T) {
	for _, hostname := range []string{"www.intel.com", "download.amd.com", "EXAMPLE.COM."} {
		if !validRealityTargetHostname(hostname) {
			t.Fatalf("valid .com hostname %q was rejected", hostname)
		}
	}
	for _, hostname := range []string{"example.test", "example.net", "com", "bad..com"} {
		if validRealityTargetHostname(hostname) {
			t.Fatalf("non-.com or invalid hostname %q was accepted", hostname)
		}
	}
}

func TestDisabledRealityInboundRemainsRecoverableWhileGuardNeedsHardening(t *testing.T) {
	if status := observedThreeXUIServiceStatus(false, "vless/tcp/reality", "action_required"); status != "degraded" {
		t.Fatalf("recoverable REALITY status = %q", status)
	}
	if status := observedThreeXUIServiceStatus(false, "vless/tcp/reality", "ready"); status != "stopped" {
		t.Fatalf("operator-stopped REALITY status = %q", status)
	}
}

func TestAgentEnrollmentTargetsSelectedSite(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	site, err := store.CreateSite(ctx, SiteInput{Name: "Singapore", Code: "singapore", Timezone: "Asia/Singapore"})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := store.CreateAgentEnrollment(ctx, AgentEnrollmentSpec{SiteID: site.ID, Name: "sg-node", CenterURL: "https://center.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.SiteID != site.ID {
		t.Fatalf("enrollment site = %q, want %q", enrollment.SiteID, site.ID)
	}
	if _, err := store.EnrollAgent(ctx, enrollment.Token, "test", "linux", "amd64", testAgentPublicKey(t)); err != nil {
		t.Fatal(err)
	}
	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].SiteID != site.ID || agents[0].Name != "sg-node" || !agents[0].Capabilities.Docker || !containsString(agents[0].Roles, "worker") {
		t.Fatalf("Agent did not join selected Site: %#v", agents)
	}
	if _, err := store.EnrollAgent(ctx, enrollment.Token, "test", "linux", "amd64", testAgentPublicKey(t)); err == nil {
		t.Fatal("single-use enrollment token was accepted twice")
	}
}

func TestDisabledAgentCredentialIsRevoked(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "retired", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.80", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}})
	if err := store.DisableAgent(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{Version: "test", Roles: []string{"worker"}, Capabilities: NodeCapabilities{Docker: true}}); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("disabled Agent credential remained usable: %v", err)
	}
	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Status != "disabled" || agents[0].Connected {
		t.Fatalf("disabled Agent state is incorrect: %#v", agents)
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Config: json.RawMessage(`{"debug":false}`)}); err == nil {
		t.Fatal("disabled Agent accepted a deployment")
	}
	if _, err := store.ConfirmNetworkProfile(ctx, node.ID, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}}); err == nil {
		t.Fatal("disabled Agent accepted a network profile update")
	}
	if _, err := store.CreateHeadscaleJoin(ctx, node.ID); err == nil {
		t.Fatal("disabled Agent accepted a Headscale join request")
	}
}
