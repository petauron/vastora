package center

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestFirstConfirmedGatewayIsSelectedForItsSite(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	node := enrollOrchestrationNode(t, store, "first-gateway", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.60", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.60", LANAddress: "10.0.0.60", EnabledKinds: []string{networking.KindLAN}})
	site, err := store.Site(context.Background(), testSiteID(t, store))
	if err != nil {
		t.Fatal(err)
	}
	if len(site.GatewayNodes) != 1 || site.GatewayNodes[0] != node.ID {
		t.Fatalf("first confirmed gateway was not selected: %#v", site.GatewayNodes)
	}
}

func TestCoLocatedGatewayDesiredStateOwnsBundledSystemRoutes(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	configureBuiltinHeadscaleForTest(t, store)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?), (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, agentConnectionModeSetting, "headscale", agentConnectURLSetting, "https://center.example.test"); err != nil {
		t.Fatal(err)
	}
	storeSystemCenterCertificateForTest(t, store, "center.example.test")
	if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?)`, setupGatewayBindingSetting, `{"publicAddress":"203.0.113.70","bindAddress":"10.0.0.70"}`); err != nil {
		t.Fatal(err)
	}
	store.discoverNetworkCandidates = func(now time.Time) ([]networking.Candidate, error) {
		return []networking.Candidate{{Address: "203.0.113.70", Interface: "eth0", Kind: networking.KindPublic, ObservedAt: now}, {Address: "100.64.0.70", Interface: "tailscale0", Kind: networking.KindHeadscale, ObservedAt: now}}, nil
	}
	node := enrollOrchestrationNode(t, store, "center-host", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{
		{Address: "10.0.0.70", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "100.64.0.70", Interface: "tailscale0", Kind: networking.KindHeadscale},
		{Address: "203.0.113.70", Interface: "eth0", Kind: networking.KindPublic},
	}, networking.Profile{ServiceAddress: "100.64.0.70", LANAddress: "10.0.0.70", HeadscaleAddress: "100.64.0.70", PublicAddress: "203.0.113.70", EnabledKinds: []string{networking.KindLAN, networking.KindHeadscale, networking.KindPublic}, DirectPublic: true})
	dns, err := os.ReadFile(store.dataDir + "/" + headscaleDNSFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dns), `"name": "center.example.test"`) || !strings.Contains(string(dns), `"value": "100.64.0.70"`) {
		t.Fatalf("private Center DNS record was not reconciled: %s", dns)
	}
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	task := claimTask(t, store, node)
	if task.Kind != "gateway.routes.apply" || task.GatewayState == nil {
		t.Fatalf("co-located gateway did not receive system desired state: %#v", task)
	}
	if len(task.GatewayState.Routes) != 8 || len(task.GatewayState.Listeners) != 3 {
		t.Fatalf("unexpected system gateway state: %#v", task.GatewayState)
	}
	for _, route := range task.GatewayState.Routes {
		if !route.System || !route.TLSEnabled {
			t.Fatalf("unprotected bundled route: %#v", route)
		}
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil, task.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, 'pending')`, agentDecommissionCallbackRouteMigrationSetting); err != nil {
		t.Fatal(err)
	}
	if err := store.activateAgentDecommissionCallbackRoute(ctx); err != nil {
		t.Fatal(err)
	}
	migrationTask := claimTask(t, store, node)
	callbackRoute := false
	if migrationTask.Kind == "gateway.routes.apply" && migrationTask.GatewayState != nil {
		for _, route := range migrationTask.GatewayState.Routes {
			callbackRoute = callbackRoute || route.ID == "system-agent-decommission-callback"
		}
	}
	if !callbackRoute {
		t.Fatal("gateway migration did not add the Agent cleanup callback route")
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, migrationTask.ID, migrationTask.Attempt, true, "", nil, migrationTask.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	var migrationMarkers int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE key = ?`, agentDecommissionCallbackRouteMigrationSetting).Scan(&migrationMarkers); err != nil || migrationMarkers != 0 {
		t.Fatalf("gateway migration marker count=%d err=%v", migrationMarkers, err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, 'pending')`, dockerInstallRouteMigrationSetting); err != nil {
		t.Fatal(err)
	}
	if err := store.activateDockerInstallRoute(ctx); err != nil {
		t.Fatal(err)
	}
	dockerMigrationTask := claimTask(t, store, node)
	dockerRoute := false
	if dockerMigrationTask.Kind == "gateway.routes.apply" && dockerMigrationTask.GatewayState != nil {
		for _, route := range dockerMigrationTask.GatewayState.Routes {
			dockerRoute = dockerRoute || route.ID == "system-docker-bootstrap"
		}
	}
	if !dockerRoute {
		t.Fatal("gateway migration did not add the Docker installer route")
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, dockerMigrationTask.ID, dockerMigrationTask.Attempt, true, "", nil, dockerMigrationTask.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE key = ?`, dockerInstallRouteMigrationSetting).Scan(&migrationMarkers); err != nil || migrationMarkers != 0 {
		t.Fatalf("Docker installer route migration marker count=%d err=%v", migrationMarkers, err)
	}
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{Name: "Test", Code: "test", Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	var desiredStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT desired_status FROM gateway_components WHERE gateway_node_id = ?`, node.ID).Scan(&desiredStatus); err != nil {
		t.Fatal(err)
	}
	if desiredStatus != "running" {
		t.Fatalf("removing the site Gateway stopped the co-located system gateway: %q", desiredStatus)
	}
}

func TestRealityCommandRequiresVerifiedTargetAndCreatesSeparateSNIEntry(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "edge", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.61", Interface: "eth0", Kind: networking.KindLAN}, {Address: "203.0.113.61", Interface: "eth0", Kind: networking.KindPublic}}, networking.Profile{ServiceAddress: "10.0.0.61", LANAddress: "10.0.0.61", PublicAddress: "203.0.113.61", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	deployment := seedLegacyDeployment(t, store, node, "10.0.0.61", "edge-api-token", threeXUIRoleMaster)
	command, err := createVerifiedRealityCommand(t, store, ctx, RealityCommandInput{ApplicationID: deployment.ApplicationID, RegionCode: "US", Name: "Edge", ClientName: "MacBook", Hostname: "reality.edge.site.example.test", DNSProvider: "manual", TargetHost: "www.example.com", ServerName: "www.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if task.Kind != "application.command" || task.ApplicationCommand == nil {
		t.Fatalf("unexpected command task: %#v", task)
	}
	if task.ApplicationCommand.TargetHost != "www.example.com" || task.ApplicationCommand.ServerName != "www.example.com" {
		t.Fatalf("explicit REALITY target was not preserved: %#v", task.ApplicationCommand)
	}
	shareURI := "vless://f47ac10b-58cc-4372-a567-0e02b2c3d479@reality.edge.site.example.test:443?type=tcp&security=reality&flow=xtls-rprx-vision&sni=www.example.com&pbk=public-key&sid=0123456789abcdef#%F0%9F%87%BA%F0%9F%87%B8%20%E7%BE%8E%E5%9B%BD%EF%BD%9CEdge"
	result := ApplicationTaskResult{ApplicationCommand: &RealityCommandResult{Action: "create", InboundID: 9, DisplayName: "🇺🇸 美国｜Edge", ClientName: "MacBook", Listen: "10.0.0.61", Port: 443, TargetHost: "www.example.com", TargetIP: "203.0.113.10", ServerName: "www.example.com", NodeASN: 64500, TargetASN: 64500, TLS13: true, X25519: true, HTTP2: true, CertificateValid: true, GuardStatus: "ready", ProxyProtocol: true, ConnectHostname: "reality.edge.site.example.test", ShareURI: shareURI, InboundTag: task.ApplicationCommand.InboundTag, ClientCreated: true}}
	encoded, _ := json.Marshal(result)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", encoded, task.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	completed, err := store.ApplicationCommand(ctx, command.ID)
	if err != nil || completed.State != "succeeded" || !completed.ResultAvailable || completed.ServerName != "www.example.com" || completed.GuardStatus != "ready" {
		t.Fatalf("unexpected completed command: %#v err=%v", completed, err)
	}
	publications, err := store.ListPublications(ctx)
	if err != nil || len(publications) != 1 || publications[0].Hostname != "reality.edge.site.example.test" || publications[0].SNIHostname != "www.example.com" {
		t.Fatalf("connection hostname and SNI were not kept separate: %#v err=%v", publications, err)
	}
	const deliveryOwner = "test-administrator"
	const deliveryKey = "reality-result-operation-1"
	link, err := store.RevealApplicationCommandResult(ctx, command.ID, deliveryOwner, deliveryKey)
	if err != nil || link != shareURI {
		t.Fatalf("one-time link = %q, err=%v", link, err)
	}
	if replay, err := store.RevealApplicationCommandResult(ctx, command.ID, deliveryOwner, deliveryKey); err != nil || replay != shareURI {
		t.Fatalf("replayed one-time link = %q, err=%v", replay, err)
	}
	if err := store.AcknowledgeApplicationCommandResult(ctx, command.ID, deliveryOwner, deliveryKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevealApplicationCommandResult(ctx, command.ID, deliveryOwner, deliveryKey); err == nil {
		t.Fatal("acknowledged one-time link remained available")
	}
}

func TestRealityCommandAllocatesRandomHostnameInsideSingleConnectionTransaction(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "random-host-edge", NodeCapabilities{Docker: true}, []networking.Candidate{
		{Address: "10.0.0.64", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "203.0.113.64", Interface: "eth0", Kind: networking.KindPublic},
	}, networking.Profile{ServiceAddress: "10.0.0.64", LANAddress: "10.0.0.64", PublicAddress: "203.0.113.64", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{Name: "Test", Code: "test", Timezone: "UTC", DomainSuffix: "example.test"}); err != nil {
		t.Fatal(err)
	}
	deployment := seedLegacyDeployment(t, store, node, "10.0.0.64", "edge-api-token", threeXUIRoleMaster)

	deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := createVerifiedRealityCommand(t, store, deadline, RealityCommandInput{ApplicationID: deployment.ApplicationID, RegionCode: "US", Name: "Random", ClientName: "MacBook", DNSProvider: "manual", TargetHost: "www.example.com", ServerName: "www.example.com"}); err != nil {
		t.Fatalf("random hostname allocation blocked inside the command transaction: %v", err)
	}
	task := claimTask(t, store, node)
	if task.ApplicationCommand == nil || !strings.HasSuffix(task.ApplicationCommand.ConnectHostname, ".example.test") {
		t.Fatalf("random connection hostname = %#v", task.ApplicationCommand)
	}
}
