package center

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
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
	tags := []string{"tag:vastora-agent", "tag:vastora-gateway"}
	roleWrites := 0
	headscale := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/node":
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []map[string]any{{"id": "7", "nodeKey": "center-node-key", "ipAddresses": []string{"100.64.0.70"}, "tags": tags}, {"id": "8", "nodeKey": "remote-node-key", "ipAddresses": []string{"100.64.0.71"}, "tags": []string{"tag:vastora-agent", "tag:vastora-gateway"}}}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/node/7/tags":
			var input struct {
				Tags []string `json:"tags"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(input.Tags, []string{"tag:vastora-agent", "tag:vastora-gateway", "tag:vastora-center"}) {
				t.Fatalf("role repair changed existing tags: %v", input.Tags)
			}
			tags = input.Tags
			roleWrites++
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected role request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer headscale.Close()
	store.headscaleHTTPClient = headscale.Client()
	store.builtinHeadscaleDialAddress = headscale.Listener.Addr().String()
	if _, err := store.db.ExecContext(ctx, `UPDATE network_integrations SET endpoint=? WHERE kind='headscale'`, headscale.URL); err != nil {
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
	if err := store.reconcileHeadscaleDNS(ctx); err != nil {
		t.Fatal(err)
	}
	if roleWrites != 1 {
		t.Fatalf("Center role repair must be idempotent: %d writes", roleWrites)
	}
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

func TestRetiredRealityCommandRequiresVerifiedTargetAndCreatesSeparateSNIEntry(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "edge", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.61", Interface: "eth0", Kind: networking.KindLAN}, {Address: "203.0.113.61", Interface: "eth0", Kind: networking.KindPublic}}, networking.Profile{ServiceAddress: "10.0.0.61", LANAddress: "10.0.0.61", PublicAddress: "203.0.113.61", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	deployment := seedLegacyDeployment(t, store, node, "10.0.0.61", "edge-api-token", threeXUIRoleMaster)
	command, err := createVerifiedRealityCommand(t, store, ctx, RealityCommandInput{ApplicationID: deployment.ApplicationID, RegionCode: "US", Name: "Edge", ClientName: "MacBook", Hostname: "reality.edge.site.example.test", DNSProvider: "manual", TargetHost: "www.example.com", ServerName: "www.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	assertRetiredProxyCommandChannel(t, store, node.ID)
}

func TestRetiredRealityCommandAllocatesRandomHostnameInsideSingleConnectionTransaction(t *testing.T) {
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
	assertRetiredProxyCommandChannel(t, store, node.ID)
}
