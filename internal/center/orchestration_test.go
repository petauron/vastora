package center

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

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

	if _, err := store.UpdateSite(ctx, defaultSiteID, SiteInput{Name: "Lab", Code: "lab", DomainSuffix: "apps.example.test", GatewayNodes: []string{node.ID}}); err != nil {
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
}

func TestPublicationCanUseGatewayOnAnotherNode(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	worker := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.20.0.11", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.20.0.11", LANAddress: "10.20.0.11", EnabledKinds: []string{networking.KindLAN}})
	gateway := enrollOrchestrationNode(t, store, "gateway", NodeCapabilities{Gateway: true}, []networking.Candidate{{Address: "10.20.0.12", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.20.0.12", LANAddress: "10.20.0.12", EnabledKinds: []string{networking.KindLAN}})
	if _, err := store.UpdateSite(ctx, defaultSiteID, SiteInput{Name: "Lab", Code: "lab", DomainSuffix: "apps.example.test", GatewayNodes: []string{gateway.ID}}); err != nil {
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

func enrollOrchestrationNode(t *testing.T, store *Store, name string, capabilities NodeCapabilities, candidates []networking.Candidate, profile networking.Profile) AgentCredential {
	t.Helper()
	ctx := context.Background()
	enrollment, err := store.CreateAgentEnrollment(ctx, defaultSiteID)
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.EnrollAgent(ctx, enrollment.Token, name, "test")
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

func TestAgentEnrollmentTargetsSelectedSite(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	site, err := store.CreateSite(ctx, SiteInput{Name: "Singapore", Code: "singapore"})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := store.CreateAgentEnrollment(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.SiteID != site.ID {
		t.Fatalf("enrollment site = %q, want %q", enrollment.SiteID, site.ID)
	}
	if _, err := store.EnrollAgent(ctx, enrollment.Token, "sg-node", "test"); err != nil {
		t.Fatal(err)
	}
	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].SiteID != site.ID {
		t.Fatalf("Agent did not join selected Site: %#v", agents)
	}
	if _, err := store.EnrollAgent(ctx, enrollment.Token, "duplicate", "test"); err == nil {
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
