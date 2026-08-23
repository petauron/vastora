package center

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestApplicationInstallAndPublicationAreIndependent(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "all-in-one", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{
		{Address: "192.168.50.10", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN},
		{Address: "100.64.0.10", Interface: "tailscale0", Family: "ipv4", Kind: networking.KindHeadscale},
	}, networking.Profile{ServiceAddress: "100.64.0.10", LANAddress: "192.168.50.10", HeadscaleAddress: "100.64.0.10", EnabledKinds: []string{networking.KindLAN, networking.KindHeadscale}})

	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{Name: "Lab", Code: "lab", Timezone: "UTC", DomainSuffix: "apps.example.test", GatewayNodes: []string{node.ID}}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	applicationID := installCPA(t, store, node, "100.64.0.10")

	services, err := store.ListServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || services[0].ApplicationID != applicationID || services[0].Status != "ready" {
		t.Fatalf("unexpected private service: %#v", services)
	}
	if routes, err := store.ListRoutes(ctx); err != nil || len(routes) != 0 {
		t.Fatalf("install unexpectedly published a route: routes=%#v err=%v", routes, err)
	}

	lan, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationLAN, GatewayNodeID: node.ID, Hostname: "cpa.lan.example.test", DNSProvider: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	headscale, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationHeadscale, GatewayNodeID: node.ID, Hostname: "cpa.tail.example.test", DNSProvider: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	publications, err := store.ListPublications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(publications) != 2 || lan.ID == headscale.ID {
		t.Fatalf("service did not retain two independent publications: %#v", publications)
	}
	task := claimTask(t, store, node)
	if task.Kind != "gateway.routes.apply" || task.GatewayState == nil || len(task.GatewayState.Routes) != 2 || len(task.GatewayState.Listeners) != 2 {
		t.Fatalf("unexpected multi-network gateway desired state: %#v", task)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil); err != nil {
		t.Fatal(err)
	}
	publications, err = store.ListPublications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, publication := range publications {
		if publication.Status != "ready" || publication.AccessURL == "" || publication.DNSRecord == nil {
			t.Fatalf("publication did not become ready: %#v", publication)
		}
	}
	if err := store.StopPublication(ctx, lan.ID); err != nil {
		t.Fatal(err)
	}
	publications, _ = store.ListPublications(ctx)
	var stopped, ready int
	for _, publication := range publications {
		if publication.Status == "stopped" {
			stopped++
		}
		if publication.Status == "ready" {
			ready++
		}
	}
	if stopped != 1 || ready != 1 {
		t.Fatalf("stopping one publication affected its sibling: %#v", publications)
	}
	reactivated, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationLAN, GatewayNodeID: node.ID, Hostname: "cpa.lan.example.test", DNSProvider: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if reactivated.ID != lan.ID || reactivated.Status != "pending" || reactivated.DesiredRevision <= lan.DesiredRevision {
		t.Fatalf("stopped publication was not safely reactivated: before=%#v after=%#v", lan, reactivated)
	}
}

func TestShared443AddsHAProxyInFrontOfCaddy(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "public-gateway", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.10", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}, {Address: "203.0.113.10", Interface: "eth0", Family: "ipv4", Kind: networking.KindPublic}}, networking.Profile{ServiceAddress: "10.0.0.10", LANAddress: "10.0.0.10", PublicAddress: "203.0.113.10", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{Name: "Public", Code: "public", Timezone: "UTC", DomainSuffix: "example.test", GatewayNodes: []string{node.ID}}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	applicationID := installCPA(t, store, node, "10.0.0.10")
	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 1 {
		t.Fatalf("unexpected Web service: %#v err=%v", services, err)
	}
	if _, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationPublic, GatewayNodeID: node.ID, Hostname: "center.example.test", DNSProvider: "manual", ConfirmHighRisk: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, app_protocol, observed_listen, status, created_at, updated_at)
		VALUES('vless-service', ?, ?, 'vless', 'tcp', 2443, 2443, '10.0.0.10:2443', 'observed', 'vless/tcp', '10.0.0.10', 'ready', ?, ?)`, applicationID, testSiteID(t, store), now, now); err != nil {
		t.Fatal(err)
	}
	shared, err := store.CreatePublication(ctx, PublicationInput{ServiceID: "vless-service", Kind: publicationShared443, GatewayNodeID: node.ID, Hostname: "vless.example.test", SNIHostname: "www.example.test", DNSProvider: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if task.Kind != "gateway.routes.apply" || task.GatewayState == nil || task.GatewayState.SharedHTTPS == nil {
		t.Fatalf("shared 443 did not queue a combined gateway state: %#v", task)
	}
	sharedState := task.GatewayState.SharedHTTPS
	if sharedState.Address != "203.0.113.10" || sharedState.Port != 443 || sharedState.CaddyAddress != "127.0.0.1" || sharedState.CaddyPort != 8443 || len(sharedState.Routes) != 1 || sharedState.Routes[0].Hostname != "www.example.test" {
		t.Fatalf("unexpected shared 443 desired state: %#v", sharedState)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil); err != nil {
		t.Fatal(err)
	}
	publication, err := store.Publication(ctx, shared.ID)
	if err != nil || publication.Status != "applying" || publication.Hostname != "vless.example.test" || publication.SNIHostname != "www.example.test" {
		t.Fatalf("shared publication did not wait for external verification: %#v err=%v", publication, err)
	}
}

func TestShared443RejectsAnApplicationAlreadyUsing443(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "public-gateway", NodeCapabilities{Gateway: true}, []networking.Candidate{{Address: "10.0.0.20", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}, {Address: "203.0.113.20", Interface: "eth0", Family: "ipv4", Kind: networking.KindPublic}}, networking.Profile{ServiceAddress: "10.0.0.20", LANAddress: "10.0.0.20", PublicAddress: "203.0.113.20", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{Name: "Public", Code: "public", Timezone: "UTC", GatewayNodes: []string{node.ID}}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, status, created_at, updated_at) VALUES('xray-app', 'Xray', ?, ?, 'test/xray', 'running', ?, ?)`, node.ID, testSiteID(t, store), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, app_protocol, observed_listen, status, created_at, updated_at)
		VALUES('vless-443', 'xray-app', ?, 'vless', 'tcp', 443, 443, '10.0.0.20:443', 'observed', 'vless/tcp', '0.0.0.0', 'ready', ?, ?)`, testSiteID(t, store), now, now); err != nil {
		t.Fatal(err)
	}
	_, err := store.CreatePublication(ctx, PublicationInput{ServiceID: "vless-443", Kind: publicationShared443, GatewayNodeID: node.ID, Hostname: "vless.example.test", SNIHostname: "www.example.test", DNSProvider: "manual"})
	if err == nil || !strings.Contains(err.Error(), "away from port 443") {
		t.Fatalf("shared 443 accepted an occupied local port: %v", err)
	}
}

func TestUninstallRemovesManagedHeadscaleDNS(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	configureBuiltinHeadscaleForTest(t, store)
	node := enrollOrchestrationNode(t, store, "all-in-one", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "100.64.0.40", Interface: "tailscale0", Family: "ipv4", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.40", HeadscaleAddress: "100.64.0.40", EnabledKinds: []string{networking.KindHeadscale}})
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{Name: "Lab", Code: "lab", Timezone: "UTC", DomainSuffix: "apps.example.test", GatewayNodes: []string{node.ID}}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	installCPA(t, store, node, "100.64.0.40")
	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 1 {
		t.Fatalf("unexpected services: %#v err=%v", services, err)
	}
	if _, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationHeadscale, GatewayNodeID: node.ID, Hostname: "cpa.tail.example.test", DNSProvider: "headscale"}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "gateway.routes.apply", nil)
	before, err := os.ReadFile(store.dataDir + "/" + headscaleDNSFile)
	if err != nil || !strings.Contains(string(before), "cpa.tail.example.test") {
		t.Fatalf("managed Headscale record was not created: %s err=%v", before, err)
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Operation: "uninstall"}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "application.apply", nil)
	after, err := os.ReadFile(store.dataDir + "/" + headscaleDNSFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "cpa.tail.example.test") || strings.TrimSpace(string(after)) != "[]" {
		t.Fatalf("uninstall left stale Headscale DNS: %s", after)
	}
	actions, err := store.ListActions(ctx, defaultActionLimit)
	if err != nil {
		t.Fatal(err)
	}
	foundRemoval := false
	for _, action := range actions {
		if action.Kind == "dns.record.remove" && action.Event == "succeeded" {
			foundRemoval = true
		}
	}
	if !foundRemoval {
		t.Fatalf("automatic DNS cleanup was not recorded in Actions: %#v", actions)
	}
}

func TestFailedPublicationCleanupIsPersistedAndRetried(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	configureBuiltinHeadscaleForTest(t, store)
	node := enrollOrchestrationNode(t, store, "cleanup-node", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "100.64.0.41", Interface: "tailscale0", Family: "ipv4", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.41", HeadscaleAddress: "100.64.0.41", EnabledKinds: []string{networking.KindHeadscale}})
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{Name: "Cleanup", Code: "cleanup", Timezone: "UTC", GatewayNodes: []string{node.ID}}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	installCPA(t, store, node, "100.64.0.41")
	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 1 {
		t.Fatalf("unexpected services: %#v err=%v", services, err)
	}
	publication, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationHeadscale, GatewayNodeID: node.ID, Hostname: "retry.tail.example.test", DNSProvider: "headscale"})
	if err != nil {
		t.Fatal(err)
	}

	originalDataDir := store.dataDir
	blocker := t.TempDir() + "/not-a-directory"
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.dataDir = blocker
	if err := store.StopPublication(ctx, publication.ID); err != nil {
		t.Fatalf("durably queued cleanup should not fail the stop operation: %v", err)
	}
	var pending, attempt int
	var retryAt string
	if err := store.db.QueryRowContext(ctx, `SELECT cleanup_pending, cleanup_attempt, cleanup_retry_at FROM publications WHERE id = ?`, publication.ID).Scan(&pending, &attempt, &retryAt); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || attempt != 1 || retryAt == "" {
		t.Fatalf("failed cleanup was not scheduled: pending=%d attempt=%d retryAt=%q", pending, attempt, retryAt)
	}

	store.dataDir = originalDataDir
	future := time.Now().UTC().Add(2 * time.Minute)
	store.now = func() time.Time { return future }
	if err := store.retryPublicationCleanups(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT cleanup_pending, cleanup_attempt, cleanup_retry_at FROM publications WHERE id = ?`, publication.ID).Scan(&pending, &attempt, &retryAt); err != nil {
		t.Fatal(err)
	}
	if pending != 0 || attempt != 0 || retryAt != "" {
		t.Fatalf("successful cleanup retry was not finalized: pending=%d attempt=%d retryAt=%q", pending, attempt, retryAt)
	}
}

func TestPublicationCanUseGatewayOnAnotherNode(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	worker := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.20.0.11", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.20.0.11", LANAddress: "10.20.0.11", EnabledKinds: []string{networking.KindLAN}})
	gateway := enrollOrchestrationNode(t, store, "gateway", NodeCapabilities{Gateway: true}, []networking.Candidate{{Address: "10.20.0.12", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.20.0.12", LANAddress: "10.20.0.12", EnabledKinds: []string{networking.KindLAN}})
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{Name: "Lab", Code: "lab", Timezone: "UTC", DomainSuffix: "apps.example.test", GatewayNodes: []string{gateway.ID}}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, gateway, "gateway.component.apply", nil)
	installCPA(t, store, worker, "10.20.0.11")
	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 1 {
		t.Fatalf("unexpected services: %#v err=%v", services, err)
	}
	if _, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationLAN, GatewayNodeID: gateway.ID, Hostname: "cpa.apps.example.test", DNSProvider: "manual"}); err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, gateway)
	if task.GatewayState == nil || len(task.GatewayState.Routes) != 1 || task.GatewayState.Routes[0].Upstreams[0].Address != "10.20.0.11" {
		t.Fatalf("cross-node gateway did not receive the worker upstream: %#v", task)
	}
}

func TestPublicationRejectsUnreachableCrossNodeOrigin(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	worker := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.20.0.21", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.20.0.21", LANAddress: "10.20.0.21", EnabledKinds: []string{networking.KindLAN}})
	gateway := enrollOrchestrationNode(t, store, "gateway", NodeCapabilities{Gateway: true}, []networking.Candidate{{Address: "10.20.0.22", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.20.0.22", LANAddress: "10.20.0.22", EnabledKinds: []string{networking.KindLAN}})
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{Name: "Lab", Code: "lab", Timezone: "UTC", DomainSuffix: "apps.example.test", GatewayNodes: []string{gateway.ID}}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, gateway, "gateway.component.apply", nil)
	installCPA(t, store, worker, "10.20.0.21")
	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 1 {
		t.Fatalf("unexpected services: %#v err=%v", services, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE services SET endpoint = '127.0.0.1:8317' WHERE id = ?`, services[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationLAN, GatewayNodeID: gateway.ID, Hostname: "cpa.apps.example.test", DNSProvider: "manual"}); err == nil || !strings.Contains(err.Error(), "routable private service address") {
		t.Fatalf("cross-node loopback origin was accepted: %v", err)
	}
}

func TestPublicationRejectsCrossNodeWithoutSharedPrivateNetwork(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	worker := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "100.64.0.21", Interface: "tailscale0", Family: "ipv4", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.21", HeadscaleAddress: "100.64.0.21", EnabledKinds: []string{networking.KindHeadscale}})
	gateway := enrollOrchestrationNode(t, store, "gateway", NodeCapabilities{Gateway: true}, []networking.Candidate{{Address: "10.20.0.32", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.20.0.32", LANAddress: "10.20.0.32", EnabledKinds: []string{networking.KindLAN}})
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{Name: "Lab", Code: "lab", Timezone: "UTC", DomainSuffix: "apps.example.test", GatewayNodes: []string{gateway.ID}}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, gateway, "gateway.component.apply", nil)
	installCPA(t, store, worker, "100.64.0.21")
	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 1 {
		t.Fatalf("unexpected services: %#v err=%v", services, err)
	}
	if _, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationLAN, GatewayNodeID: gateway.ID, Hostname: "cpa.apps.example.test", DNSProvider: "manual"}); err == nil || !strings.Contains(err.Error(), "cannot reach") {
		t.Fatalf("cross-node publication without a shared private network was accepted: %v", err)
	}
}

func installCPA(t *testing.T, store *Store, node AgentCredential, address string) string {
	t.Helper()
	deployment, err := store.CreateDeployment(context.Background(), DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Config: json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"services":[{"name":"api","protocol":"http","containerPort":8317,"hostPort":8317,"address":"` + address + `"}]}`)
	completeNextTask(t, store, node, "application.apply", result)
	return deployment.ApplicationID
}

func claimTask(t *testing.T, store *Store, node AgentCredential) *AgentTask {
	t.Helper()
	task, err := store.ClaimNextTask(context.Background(), node.ID, node.Credential)
	if err != nil {
		t.Fatal(err)
	}
	if task == nil {
		t.Fatal("expected an Agent task")
	}
	return task
}

func completeNextTask(t *testing.T, store *Store, node AgentCredential, kind string, result json.RawMessage) {
	t.Helper()
	task := claimTask(t, store, node)
	if task.Kind != kind {
		t.Fatalf("got task kind %q, want %q", task.Kind, kind)
	}
	if err := store.CompleteTask(context.Background(), node.ID, node.Credential, task.ID, task.Attempt, true, "", result); err != nil {
		t.Fatal(err)
	}
}

func openOrchestrationStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile("../../catalog/catalog.json")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.SeedOfficialCatalog(context.Background(), payload); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store
}

func configureBuiltinHeadscaleForTest(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	secretID, err := store.putSecret(ctx, tx, []byte("test-headscale-api-key"), "integration:headscale")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO network_integrations(kind, mode, endpoint, secret_id, status, created_at, updated_at) VALUES('headscale', 'builtin', 'https://headscale.example.test', ?, 'configured', ?, ?)`, secretID, now, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func enrollOrchestrationNode(t *testing.T, store *Store, name string, capabilities NodeCapabilities, candidates []networking.Candidate, profile networking.Profile) AgentCredential {
	t.Helper()
	ctx := context.Background()
	enrollment, err := store.CreateAgentEnrollment(ctx, AgentEnrollmentSpec{SiteID: testSiteID(t, store), Name: name, CenterURL: "https://center.example.com", Gateway: capabilities.Gateway, Tunnel: capabilities.Tunnel})
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.EnrollAgent(ctx, enrollment.Token, "test")
	if err != nil {
		t.Fatal(err)
	}
	roles := []string{}
	if capabilities.Docker || capabilities.Tunnel {
		roles = append(roles, "worker")
	}
	if capabilities.Gateway {
		roles = append(roles, "gateway")
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{Version: "test", Roles: roles, Capabilities: capabilities, NetworkCandidates: candidates, GatewayHealthy: capabilities.Gateway}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfirmNetworkProfile(ctx, node.ID, profile); err != nil {
		t.Fatal(err)
	}
	return node
}

func TestFirstConfirmedGatewayIsSelectedForItsSite(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	node := enrollOrchestrationNode(t, store, "first-gateway", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.60", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.60", LANAddress: "10.0.0.60", EnabledKinds: []string{networking.KindLAN}})
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
	store.discoverNetworkCandidates = func(now time.Time) ([]networking.Candidate, error) {
		return []networking.Candidate{{Address: "203.0.113.70", Interface: "eth0", Family: "ipv4", Kind: networking.KindPublic, ObservedAt: now}, {Address: "100.64.0.70", Interface: "tailscale0", Family: "ipv4", Kind: networking.KindHeadscale, ObservedAt: now}}, nil
	}
	node := enrollOrchestrationNode(t, store, "center-host", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{
		{Address: "10.0.0.70", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN},
		{Address: "100.64.0.70", Interface: "tailscale0", Family: "ipv4", Kind: networking.KindHeadscale},
		{Address: "203.0.113.70", Interface: "eth0", Family: "ipv4", Kind: networking.KindPublic},
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
	if len(task.GatewayState.Routes) != 5 || len(task.GatewayState.Listeners) != 3 {
		t.Fatalf("unexpected system gateway state: %#v", task.GatewayState)
	}
	for _, route := range task.GatewayState.Routes {
		if !route.System || !route.TLSEnabled {
			t.Fatalf("unprotected bundled route: %#v", route)
		}
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil); err != nil {
		t.Fatal(err)
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

func TestRealityCommandCreatesObservedInboundAndSeparateSNIEntry(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "edge", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.61", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}, {Address: "203.0.113.61", Interface: "eth0", Family: "ipv4", Kind: networking.KindPublic}}, networking.Profile{ServiceAddress: "10.0.0.61", LANAddress: "10.0.0.61", PublicAddress: "203.0.113.61", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleMaster, Config: json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	installTask := claimTask(t, store, node)
	completeThreeXUIDeployment(t, store, node, installTask, "10.0.0.61", "edge-api-token")
	command, err := store.CreateRealityCommand(ctx, RealityCommandInput{ApplicationID: deployment.ApplicationID, Name: "MacBook", GatewayNodeID: node.ID, Hostname: "reality.edge.site.example.test", DNSProvider: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if task.Kind != "application.command" || task.ApplicationCommand == nil {
		t.Fatalf("unexpected command task: %#v", task)
	}
	shareURI := "vless://f47ac10b-58cc-4372-a567-0e02b2c3d479@reality.edge.site.example.test:443?type=tcp&security=reality&flow=xtls-rprx-vision&sni=www.example.com&pbk=public-key&sid=0123456789abcdef#MacBook"
	result := ApplicationTaskResult{ApplicationCommand: &RealityCommandResult{InboundID: 9, Name: "MacBook", Listen: "10.0.0.61", Port: 35443, Target: "www.example.com:443", SNIHostname: "www.example.com", ConnectHostname: "reality.edge.site.example.test", ShareURI: shareURI}}
	encoded, _ := json.Marshal(result)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", encoded); err != nil {
		t.Fatal(err)
	}
	completed, err := store.ApplicationCommand(ctx, command.ID)
	if err != nil || completed.State != "succeeded" || !completed.ResultAvailable || completed.SNIHostname != "www.example.com" {
		t.Fatalf("unexpected completed command: %#v err=%v", completed, err)
	}
	publications, err := store.ListPublications(ctx)
	if err != nil || len(publications) != 1 || publications[0].Hostname != "reality.edge.site.example.test" || publications[0].SNIHostname != "www.example.com" {
		t.Fatalf("connection hostname and SNI were not kept separate: %#v err=%v", publications, err)
	}
	link, err := store.ConsumeApplicationCommandResult(ctx, command.ID)
	if err != nil || link != shareURI {
		t.Fatalf("one-time link = %q, err=%v", link, err)
	}
	if _, err := store.ConsumeApplicationCommandResult(ctx, command.ID); err == nil {
		t.Fatal("one-time link was revealed twice")
	}
}

func TestSubscriptionCommandPublishesOnlyTheSubscriptionService(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "edge", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{
		{Address: "10.0.0.62", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN},
		{Address: "203.0.113.62", Interface: "eth0", Family: "ipv4", Kind: networking.KindPublic},
	}, networking.Profile{ServiceAddress: "10.0.0.62", LANAddress: "10.0.0.62", PublicAddress: "203.0.113.62", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at) VALUES('three-x-ui-subscription', '3x-ui', ?, ?, ?, '', 'running', 'docker', 'master', ?, ?)`, node.ID, testSiteID(t, store), threeXUIAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	for _, service := range []struct {
		id, name string
		port     int
		manager  int
	}{{"three-x-ui-panel", "panel", 2053, 1}, {"three-x-ui-subscription", "subscription", 2096, 0}} {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, management, status, created_at, updated_at) VALUES(?, 'three-x-ui-subscription', ?, ?, 'http', ?, ?, ?, 'catalog', ?, 'ready', ?, ?)`, service.id, testSiteID(t, store), service.name, service.port, service.port, net.JoinHostPort("10.0.0.62", fmt.Sprint(service.port)), service.manager, now, now); err != nil {
			t.Fatal(err)
		}
	}
	command, err := store.CreateSubscriptionCommand(ctx, SubscriptionCommandInput{ApplicationID: "three-x-ui-subscription", GatewayNodeID: node.ID, Hostname: "subscribe.edge.example.test", Kind: publicationPublic, DNSProvider: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if task.Kind != "application.command" || task.SubscriptionCommand == nil || task.ApplicationCommand != nil {
		t.Fatalf("unexpected subscription task: %#v", task)
	}
	if task.SubscriptionCommand.Domain != "subscribe.edge.example.test" || task.SubscriptionCommand.BaseURI != "https://subscribe.edge.example.test/sub/" {
		t.Fatalf("unexpected subscription settings: %#v", task.SubscriptionCommand)
	}
	result, _ := json.Marshal(ApplicationTaskResult{SubscriptionCommand: &SubscriptionCommandResult{Domain: task.SubscriptionCommand.Domain, BaseURI: task.SubscriptionCommand.BaseURI}})
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", result); err != nil {
		t.Fatal(err)
	}
	completed, err := store.ApplicationCommand(ctx, command.ID)
	if err != nil || completed.State != "succeeded" || completed.PublicationID == "" {
		t.Fatalf("unexpected completed subscription command: %#v err=%v", completed, err)
	}
	publication, err := store.Publication(ctx, completed.PublicationID)
	if err != nil || publication.ServiceID != "three-x-ui-subscription" || publication.TLSEnabled != true {
		t.Fatalf("subscription publication = %#v, err=%v", publication, err)
	}
	var panelPublications int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE service_id = 'three-x-ui-panel' AND status <> 'stopped'`).Scan(&panelPublications); err != nil {
		t.Fatal(err)
	}
	if panelPublications != 0 {
		t.Fatal("3x-ui management panel was published with the subscription service")
	}
}

func TestGatewayCertificatePrivateKeyIsAbsentFromDesiredStateAndActions(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "private-gateway", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.63", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.63", LANAddress: "10.0.0.63", EnabledKinds: []string{networking.KindLAN}})
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	applicationID := installCPA(t, store, node, "10.0.0.63")
	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 1 || services[0].ApplicationID != applicationID {
		t.Fatalf("services = %#v, err=%v", services, err)
	}
	publication, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationLAN, GatewayNodeID: node.ID, Hostname: "cpa.private.example.test", DNSProvider: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	certificate := managedCertificate{CertificatePEM: "TEST CERTIFICATE", PrivateKeyPEM: "TEST PRIVATE KEY", NotAfter: time.Now().UTC().Add(24 * time.Hour)}
	encoded, _ := json.Marshal(certificate)
	secretID, err := store.putSecret(ctx, tx, encoded, "publication-certificate:"+publication.ID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE publications SET certificate_secret_id = ?, certificate_not_after = ?, tls_enabled = 1 WHERE id = ?`, secretID, certificate.NotAfter.Format(time.RFC3339Nano), publication.ID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE routes SET tls_enabled = 1 WHERE publication_id = ?`, publication.ID)
	}
	if err == nil {
		err = store.queueGatewayState(ctx, tx, node.ID, time.Now().UTC())
	}
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
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
	if task.Kind != "gateway.routes.apply" || len(task.GatewayCertificates) != 1 || task.GatewayCertificates[0].PrivateKeyPEM != "TEST PRIVATE KEY" {
		t.Fatalf("certificate was not delivered only with the Agent task: %#v", task)
	}
}

func TestValidateRealityCommandResultRejectsTamperedClientLink(t *testing.T) {
	input := RealityCommandTask{Name: "MacBook", ConnectHostname: "reality.edge.site.example.test"}
	valid := RealityCommandResult{
		InboundID:       9,
		Name:            "MacBook",
		Listen:          "10.0.0.61",
		Port:            35443,
		Target:          "www.example.com:443",
		SNIHostname:     "www.example.com",
		ConnectHostname: "reality.edge.site.example.test",
		ShareURI:        "vless://f47ac10b-58cc-4372-a567-0e02b2c3d479@reality.edge.site.example.test:443?type=tcp&security=reality&flow=xtls-rprx-vision&sni=www.example.com&pbk=public-key&sid=0123456789abcdef#MacBook",
	}
	if err := validateRealityCommandResult(input, valid); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	for name, mutate := range map[string]func(*RealityCommandResult){
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
	if _, err := store.EnrollAgent(ctx, enrollment.Token, "test"); err != nil {
		t.Fatal(err)
	}
	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].SiteID != site.ID || agents[0].Name != "sg-node" || !agents[0].Capabilities.Docker || !containsString(agents[0].Roles, "worker") {
		t.Fatalf("Agent did not join selected Site: %#v", agents)
	}
	if _, err := store.EnrollAgent(ctx, enrollment.Token, "test"); err == nil {
		t.Fatal("single-use enrollment token was accepted twice")
	}
}

func TestDisabledAgentCredentialIsRevoked(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "retired", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.80", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}})
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
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Config: json.RawMessage(`{"timezone":"UTC","management_key":"management-secret","api_key":"client-secret","debug":false}`)}); err == nil {
		t.Fatal("disabled Agent accepted a deployment")
	}
	if _, err := store.ConfirmNetworkProfile(ctx, node.ID, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}}); err == nil {
		t.Fatal("disabled Agent accepted a network profile update")
	}
	if _, err := store.CreateHeadscaleJoin(ctx, node.ID); err == nil {
		t.Fatal("disabled Agent accepted a Headscale join request")
	}
}
