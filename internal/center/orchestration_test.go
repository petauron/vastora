package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
)

func TestApplicationInstallAndPublicationAreIndependent(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "all-in-one", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{
		{Address: "192.168.50.10", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "100.64.0.10", Interface: "tailscale0", Kind: networking.KindHeadscale},
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
	if len(services) != 2 || services[0].ApplicationID != applicationID || services[0].Status != "ready" {
		t.Fatalf("unexpected private service: %#v", services)
	}
	if routes, err := store.ListRoutes(ctx); err != nil || len(routes) != 0 {
		t.Fatalf("install unexpectedly published a route: routes=%#v err=%v", routes, err)
	}

	lan, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationLAN, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID}, Hostname: "cpa.lan.example.test", DNSProvider: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	headscale, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationHeadscale, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID}, Hostname: "cpa.tail.example.test", DNSProvider: "manual"})
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
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil, task.RequiredRuntimeGeneration); err != nil {
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
	reactivated, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationLAN, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID}, Hostname: "cpa.lan.example.test", DNSProvider: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if reactivated.ID != lan.ID || reactivated.Status != "pending" || reactivated.DesiredRevision <= lan.DesiredRevision {
		t.Fatalf("stopped publication was not safely reactivated: before=%#v after=%#v", lan, reactivated)
	}
}

func TestStaleGatewayCompletionIsAcknowledgedAfterDesiredStateAdvances(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "gateway-restart", NodeCapabilities{Gateway: true}, []networking.Candidate{{Address: "10.0.0.42", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.42", LANAddress: "10.0.0.42", EnabledKinds: []string{networking.KindLAN}})

	if _, err := store.db.ExecContext(ctx, `UPDATE gateway_states
		SET desired_revision = 36, applied_revision = 33, status = 'pending', attempt = 1164
		WHERE gateway_node_id = ?`, node.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteGatewayState(ctx, commitProjectionOnlyForTest, node.ID, node.Credential, 35, 1164, true, ""); err != nil {
		t.Fatalf("superseded gateway completion was not acknowledged: %v", err)
	}

	var desired, applied, attempt int64
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision, applied_revision, status, attempt FROM gateway_states WHERE gateway_node_id = ?`, node.ID).Scan(&desired, &applied, &status, &attempt); err != nil {
		t.Fatal(err)
	}
	if desired != 36 || applied != 33 || status != "pending" || attempt != 1164 {
		t.Fatalf("superseded completion changed current gateway state: desired=%d applied=%d status=%q attempt=%d", desired, applied, status, attempt)
	}
	if err := store.CompleteGatewayState(ctx, commitProjectionOnlyForTest, node.ID, node.Credential, 37, 1164, true, ""); err == nil || !strings.Contains(err.Error(), "stale gateway result") {
		t.Fatalf("future gateway completion was accepted: %v", err)
	}
}

func TestCloudflareWebPublicationRequiresConfiguredCenterAccess(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	storeCloudflareOAuthIntegration(t, store, cloudflareOAuthToken{})
	node := enrollOrchestrationNode(t, store, "tunnel-node", NodeCapabilities{Docker: true, Tunnel: true}, []networking.Candidate{{Address: "10.0.0.12", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.12", LANAddress: "10.0.0.12", EnabledKinds: []string{networking.KindLAN}})
	applicationID := installCPA(t, store, node, "10.0.0.12")
	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 2 || services[0].ApplicationID != applicationID {
		t.Fatalf("unexpected services: %#v err=%v", services, err)
	}
	_, err = store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationCloudflare, Ingress: PublicationIngress{Owner: ingressTunnelConnector, EntryNodeID: node.ID}, Hostname: "cpa-tunnel-node.example.com", DNSProvider: "cloudflare"})
	if err == nil || !strings.Contains(err.Error(), "enable the Center Cloudflare Access entry") {
		t.Fatalf("unconfigured Center Access returned the wrong publication error: %v", err)
	}
}

func TestCPAClientAPIPublicationPolicyIsSeparateFromManagement(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	storeCloudflareOAuthIntegration(t, store, cloudflareOAuthToken{})
	node := enrollOrchestrationNode(t, store, "cpa-tunnel-node", NodeCapabilities{Docker: true, Tunnel: true}, []networking.Candidate{{Address: "10.0.0.13", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.13", LANAddress: "10.0.0.13", EnabledKinds: []string{networking.KindLAN}})
	applicationID := installCPA(t, store, node, "10.0.0.13")
	services, err := store.ListServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var managementService, clientAPIService ServiceView
	for _, service := range services {
		if service.ApplicationID != applicationID {
			continue
		}
		switch service.Name {
		case "api":
			managementService = service
		case cpaClientAPIServiceName:
			clientAPIService = service
		}
	}
	if managementService.ID == "" || clientAPIService.ID == "" {
		t.Fatalf("CPA services were not separated: %#v", services)
	}
	if !cloudflareAccessRequiredForService(cpaAppKey, managementService.Name) || cloudflareAccessRequiredForService(cpaAppKey, clientAPIService.Name) {
		t.Fatal("CPA management and client API received the wrong Cloudflare Access policy")
	}
	for _, input := range []PublicationInput{
		{ServiceID: clientAPIService.ID, Kind: publicationLAN, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID}, Hostname: "cpa.private.example.test", DNSProvider: "manual"},
		{ServiceID: clientAPIService.ID, Kind: publicationCloudflare, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID}, Hostname: "cpa.example.com", DNSProvider: "cloudflare"},
	} {
		if _, err := store.CreatePublication(ctx, input); err == nil || !strings.Contains(err.Error(), "only through Cloudflare Tunnel") {
			t.Fatalf("unsupported CPA client API publication was accepted: input=%#v err=%v", input, err)
		}
	}
}

func TestAgentRuntimeGenerationQueuesOneApplicationReconcile(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "runtime-migration", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.81", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.81", LANAddress: "10.0.0.81", EnabledKinds: []string{networking.KindLAN}})
	applicationID := installCPA(t, store, node, "10.0.0.81")
	registryCredential, err := store.CreateRegistryCredential(ctx, "docker.io", "runtime-robot", "runtime-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET registry_credential_id = ? WHERE application_id = ? AND state = 'succeeded'`, registryCredential.ID, applicationID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{Version: "downgraded-runtime", Roles: []string{"worker"}, Capabilities: testRuntimeCapabilities(NodeCapabilities{Docker: true}), ApplicationRuntimeGeneration: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE applications SET runtime_generation = 0 WHERE id = ?`, applicationID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{
		Version: "new-runtime", Roles: []string{"worker"}, Capabilities: testRuntimeCapabilities(NodeCapabilities{Docker: true}),
		ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration,
	}); err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if task.Kind != "application.apply" || task.Operation != "configure" || task.AppKey != cpaAppKey || task.ServiceAddress != "10.0.0.81" || len(task.Secrets) == 0 || task.RegistryCredential == nil || task.RegistryCredential.Password != "runtime-token" {
		t.Fatalf("unexpected runtime migration task: %#v", task)
	}
	result := cpaApplicationResult("10.0.0.81")
	result = mockPackageResult(t, task, result)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", result, task.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	var generation int
	if err := store.db.QueryRowContext(ctx, `SELECT runtime_generation FROM applications WHERE id = ?`, applicationID).Scan(&generation); err != nil || generation != platform.ApplicationRuntimeGeneration {
		t.Fatalf("application runtime generation = %d, err=%v", generation, err)
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{
		Version: "new-runtime", Roles: []string{"worker"}, Capabilities: testRuntimeCapabilities(NodeCapabilities{Docker: true}),
		ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration,
	}); err != nil {
		t.Fatal(err)
	}
	if task, err := store.ClaimNextTask(ctx, node.ID, node.Credential); err != nil || task != nil {
		t.Fatalf("runtime migration was queued more than once: task=%#v err=%v", task, err)
	}
}

func TestLegacyProxyRuntimeMigrationDefersToMeridianCutover(t *testing.T) {
	store := openLegacyOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "xray-runtime-migration", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.86", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.86", LANAddress: "10.0.0.86", EnabledKinds: []string{networking.KindLAN}})

	value, _, err := readAcceptedOfficialCatalog(ctx, store.db, "stable")
	if err != nil {
		t.Fatal(err)
	}
	var current catalog.AppManifest
	for _, manifest := range value.Apps {
		if manifest.ID == "3x-ui" {
			current = manifest
			break
		}
	}
	if current.ID == "" {
		t.Fatal("3x-ui manifest not found in official catalog fixture")
	}
	legacy := current
	legacy.Version = "3.6.0"
	legacy.Images = slices.DeleteFunc(slices.Clone(current.Images), func(image catalog.Image) bool {
		return image.Name == "xray-core"
	})
	legacyJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	now := store.now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	applicationID := "xray-runtime-migration-app"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, runtime_generation, created_at, updated_at)
		VALUES(?, 'Vastora Proxy', ?, ?, ?, '', 'running', 'docker', 'worker', 1, ?, ?)`, applicationID, node.ID, testSiteID(t, store), threeXUIAppKey, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO deployments(id, agent_id, app_key, app_version, manifest_json, config_json, service_address, operation, state, created_at, updated_at, application_id, runtime_generation)
		VALUES('legacy-xray-deployment', ?, ?, ?, ?, '{}', '10.0.0.86', 'configure', 'succeeded', ?, ?, ?, 1)`, node.ID, threeXUIAppKey, legacy.Version, legacyJSON, stamp, stamp, applicationID); err != nil {
		t.Fatal(err)
	}
	secretTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	applicationSecretID, err := store.putSecret(ctx, secretTx, []byte(`{"api_token":"current-worker-token","username":"legacy-user","password":"legacy-password"}`), "application:"+applicationID)
	if err != nil {
		secretTx.Rollback()
		t.Fatal(err)
	}
	if _, err := secretTx.ExecContext(ctx, `INSERT INTO application_secrets(application_id,secret_id,updated_at) VALUES(?,?,?)`, applicationID, applicationSecretID, stamp); err != nil {
		secretTx.Rollback()
		t.Fatal(err)
	}
	if err := secretTx.Commit(); err != nil {
		t.Fatal(err)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.queueRuntimeApplicationDeployments(ctx, tx, node.ID, platform.ApplicationRuntimeGeneration, now); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var pending int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployments WHERE application_id=? AND state='pending'`, applicationID).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("ordinary runtime upgrade redeployed legacy proxy: pending=%d err=%v", pending, err)
	}
	var status, appKey string
	var generation int
	if err := store.db.QueryRowContext(ctx, `SELECT status,app_key,runtime_generation FROM applications WHERE id=?`, applicationID).Scan(&status, &appKey, &generation); err != nil {
		t.Fatal(err)
	}
	if status != "running" || appKey != threeXUIAppKey || generation != 1 {
		t.Fatalf("runtime upgrade changed legacy cutover input: status=%q app=%q generation=%d", status, appKey, generation)
	}
	var retainedSecretID string
	if err := store.db.QueryRowContext(ctx, `SELECT secret_id FROM application_secrets WHERE application_id=?`, applicationID).Scan(&retainedSecretID); err != nil || retainedSecretID != applicationSecretID {
		t.Fatalf("runtime upgrade changed legacy secrets: err=%v", err)
	}
}

func TestAgentRuntimeGenerationFencesClaimsAndResultEvidence(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "runtime-fence", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.83", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.83", LANAddress: "10.0.0.83", EnabledKinds: []string{networking.KindLAN}})
	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"debug":false}`), AuthorizedCapabilities: testCapabilityGrant("root")})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{Version: "downgraded-runtime", Roles: []string{"worker"}, Capabilities: testRuntimeCapabilities(NodeCapabilities{Docker: true}), ApplicationRuntimeGeneration: 0}); err != nil {
		t.Fatal(err)
	}
	if task, err := store.ClaimNextTask(ctx, node.ID, node.Credential); err != nil || task != nil {
		t.Fatalf("generation-zero Agent claim = %#v, err=%v", task, err)
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{Version: "current-runtime", Roles: []string{"worker"}, Capabilities: testRuntimeCapabilities(NodeCapabilities{Docker: true}), ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration}); err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	result := cpaApplicationResult("10.0.0.83")
	result = mockPackageResult(t, task, result)
	if err := store.completeTaskWithDisposition(ctx, commitProjectionOnlyForTest, node.ID, node.Credential, task.ID, task.Attempt, true, "", result, false); err == nil || !strings.Contains(err.Error(), "missing application runtime generation") {
		t.Fatalf("result without executor generation was accepted: %v", err)
	}
	if err := store.completeTaskWithDisposition(ctx, commitProjectionOnlyForTest, node.ID, node.Credential, task.ID, task.Attempt, true, "", result, false, 0); err == nil || !strings.Contains(err.Error(), "runtime generation") {
		t.Fatalf("generation-zero result was accepted: %v", err)
	}
	var state string
	var executedGeneration sql.NullInt64
	if err := store.db.QueryRowContext(ctx, `SELECT state, executed_runtime_generation FROM deployments WHERE id = ?`, deployment.ID).Scan(&state, &executedGeneration); err != nil || state != "running" || executedGeneration.Valid {
		t.Fatalf("rejected result changed deployment: state=%q executed=%#v err=%v", state, executedGeneration, err)
	}
	if err := store.completeTaskWithDisposition(ctx, commitProjectionOnlyForTest, node.ID, node.Credential, task.ID, task.Attempt, true, "", result, false, platform.ApplicationRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT state, executed_runtime_generation FROM deployments WHERE id = ?`, deployment.ID).Scan(&state, &executedGeneration); err != nil || state != "succeeded" || !executedGeneration.Valid || executedGeneration.Int64 != platform.ApplicationRuntimeGeneration {
		t.Fatalf("verified result evidence was not persisted: state=%q executed=%#v err=%v", state, executedGeneration, err)
	}
}

func TestNewerAgentCompletesOlderPendingRuntimeTaskAtExecutedGeneration(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "runtime-forward-executor", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.85", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.85", LANAddress: "10.0.0.85", EnabledKinds: []string{networking.KindLAN}})
	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"debug":false}`), AuthorizedCapabilities: testCapabilityGrant("root")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET runtime_generation = 0 WHERE id = ?`, deployment.ID); err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if task.RequiredRuntimeGeneration != 0 {
		t.Fatalf("required runtime generation = %d", task.RequiredRuntimeGeneration)
	}
	result := cpaApplicationResult("10.0.0.85")
	result = mockPackageResult(t, task, result)
	if err := store.completeTaskWithDisposition(ctx, commitProjectionOnlyForTest, node.ID, node.Credential, task.ID, task.Attempt, true, "", result, false, platform.ApplicationRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	var generation int
	if err := store.db.QueryRowContext(ctx, `SELECT runtime_generation FROM applications WHERE id = ?`, deployment.ApplicationID).Scan(&generation); err != nil || generation != platform.ApplicationRuntimeGeneration {
		t.Fatalf("application executed runtime generation = %d, err=%v", generation, err)
	}
}

func TestAgentRuntimeMigrationDoesNotReplaceFailedOperation(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "runtime-level-trigger", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.84", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.84", LANAddress: "10.0.0.84", EnabledKinds: []string{networking.KindLAN}})
	applicationID := installCPA(t, store, node, "10.0.0.84")
	if _, err := store.db.ExecContext(ctx, `UPDATE agents SET runtime_generation = 0 WHERE id = ?`, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE applications SET runtime_generation = 0 WHERE id = ?`, applicationID); err != nil {
		t.Fatal(err)
	}
	blocker, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Operation: "configure", Config: json.RawMessage(`{"debug":true}`), AuthorizedCapabilities: testCapabilityGrant("root")})
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := NodeHeartbeat{Version: "new-runtime", Roles: []string{"worker"}, Capabilities: testRuntimeCapabilities(NodeCapabilities{Docker: true}), ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET state = 'failed' WHERE id = ?`, blocker.ID); err != nil {
		t.Fatal(err)
	}
	// Even if the old application still runs, a later failed configuration
	// cannot be silently replaced with the last successful configuration.
	if _, err := store.db.ExecContext(ctx, `UPDATE applications SET status='running' WHERE id=?`, applicationID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployments WHERE application_id = ? AND state = 'pending' AND runtime_generation = ?`, applicationID, platform.ApplicationRuntimeGeneration).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("failed operation replaced by runtime migration: %d, err=%v", queued, err)
	}
}

func TestAgentRuntimeGenerationRecreatesGateway(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "runtime-gateway", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.82", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.82", LANAddress: "10.0.0.82", EnabledKinds: []string{networking.KindLAN}})
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	completeNextTask(t, store, node, "gateway.routes.apply", nil)
	if _, err := store.db.ExecContext(ctx, `UPDATE agents SET runtime_generation = 0 WHERE id = ?`, node.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{
		Version: "new-runtime", Roles: []string{"worker", "gateway"}, Capabilities: testRuntimeCapabilities(NodeCapabilities{Docker: true, Gateway: true}), GatewayHealthy: true,
		ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration,
	}); err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if task.Kind != "gateway.component.apply" {
		t.Fatalf("gateway runtime was not recreated first: %#v", task)
	}
}

func TestAgentRuntimeGenerationPreservesInstalledKomariManifest(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "runtime-komari", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.83", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.83", LANAddress: "10.0.0.83", EnabledKinds: []string{networking.KindLAN}})
	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: komariAppKey, Config: json.RawMessage(`{"endpoint":"https://komari.example.test","token":"secret-token"}`)})
	if err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "application.apply", json.RawMessage(`{"services":[]}`))
	if _, err := store.db.ExecContext(ctx, `UPDATE agents SET runtime_generation = 0 WHERE id = ?`, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE applications SET runtime_generation = 0 WHERE id = ?`, deployment.ApplicationID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{
		Version: "new-runtime", Roles: []string{"worker"}, Capabilities: testRuntimeCapabilities(NodeCapabilities{Docker: true}),
		ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration,
	}); err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if task.AppKey != komariAppKey || task.Operation != "configure" || len(task.Manifest.Images) != 0 || len(task.Manifest.Artifacts) != 2 {
		t.Fatalf("runtime update changed Komari's installed native contract: %#v", task.Manifest)
	}
}

func TestShared443AddsHAProxyInFrontOfCaddy(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	store.publicationVerificationBackoff = func(int) time.Duration { return 0 }
	var sharedPublicationID string
	var verificationAttempts atomic.Int32
	verificationDone := make(chan struct{})
	firstObservedStatus := make(chan string, 1)
	store.verifyPublication = func(ctx context.Context, id string, _ int64) (PublicationView, error) {
		if id != sharedPublicationID {
			return store.markPublicationReady(ctx, id, 0)
		}
		attempt := verificationAttempts.Add(1)
		if attempt < 3 {
			value, err := store.Publication(ctx, id)
			if err == nil {
				if attempt == 1 {
					firstObservedStatus <- value.Status
				}
				value.Status = "pending"
				value.LastError = "DNS record has not propagated"
			}
			return value, err
		}
		value, err := store.markPublicationReady(ctx, id, 0)
		if err == nil {
			close(verificationDone)
		}
		return value, err
	}
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "public-gateway", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "10.0.0.10", Interface: "eth0", Kind: networking.KindLAN}, {Address: "203.0.113.10", Interface: "eth0", Kind: networking.KindPublic}}, networking.Profile{ServiceAddress: "10.0.0.10", LANAddress: "10.0.0.10", PublicAddress: "203.0.113.10", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{Name: "Public", Code: "public", Timezone: "UTC", DomainSuffix: "example.test", GatewayNodes: []string{node.ID}}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	applicationID := installCPA(t, store, node, "10.0.0.10")
	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 2 {
		t.Fatalf("unexpected Web service: %#v err=%v", services, err)
	}
	if _, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationPublic, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID}, Hostname: "center.example.test", DNSProvider: "manual", ConfirmHighRisk: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, app_protocol, observed_listen, status, created_at, updated_at)
		VALUES('vless-service', ?, ?, 'vless', 'tcp', 2443, 2443, '10.0.0.10:2443', 'observed', 'vless/tcp', '10.0.0.10', 'ready', ?, ?)`, applicationID, testSiteID(t, store), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePublication(ctx, PublicationInput{ServiceID: "vless-service", Kind: publicationShared443, Ingress: PublicationIngress{Owner: ingressApplicationNode, EntryNodeID: node.ID}, Hostname: "caller-selected.example.test", SNIHostname: "www.example.test", DNSProvider: "manual"}); err == nil || !strings.Contains(err.Error(), "does not accept an entry node") {
		t.Fatalf("application-node publication accepted caller-selected entry: %v", err)
	}
	shared, err := store.CreatePublication(ctx, PublicationInput{ServiceID: "vless-service", Kind: publicationShared443, Ingress: PublicationIngress{Owner: ingressApplicationNode}, Hostname: "vless.example.test", SNIHostname: "www.example.test", DNSProvider: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	sharedPublicationID = shared.ID
	if _, err := store.db.ExecContext(ctx, `UPDATE publications SET dns_provider = 'cloudflare', status = 'failed', last_error = 'Cloudflare DNS unavailable' WHERE id = ?`, shared.ID); err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if task.Kind != "node.listener.apply" || task.NodeListenerState == nil {
		t.Fatalf("shared 443 did not queue an application-node listener state: %#v", task)
	}
	sharedState := &task.NodeListenerState.Listener
	if sharedState.Address != "203.0.113.10" || sharedState.Port != 443 || sharedState.CaddyAddress != "vastora-gateway-caddy" || sharedState.CaddyPort != 443 || sharedState.RejectUnmatched || len(sharedState.Routes) != 1 || sharedState.Routes[0].Hostname != "www.example.test" {
		t.Fatalf("unexpected shared 443 desired state: %#v", sharedState)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil, task.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	select {
	case <-verificationDone:
	case <-time.After(time.Second):
		t.Fatal("shared 443 publication was not verified after node-listener apply")
	}
	if status := <-firstObservedStatus; status != "failed" {
		t.Fatalf("node-listener success discarded the managed DNS failure before retry: %q", status)
	}
	publication, err := store.Publication(ctx, shared.ID)
	if err != nil || publication.Status != "ready" || publication.Hostname != "vless.example.test" || publication.SNIHostname != "www.example.test" || verificationAttempts.Load() != 3 {
		t.Fatalf("shared publication did not converge after automatic verification: publication=%#v attempts=%d err=%v", publication, verificationAttempts.Load(), err)
	}
	var gatewayRevisionBefore int64
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision FROM gateway_states WHERE gateway_node_id = ?`, node.ID).Scan(&gatewayRevisionBefore); err != nil {
		t.Fatal(err)
	}
	if err := store.StopPublication(ctx, shared.ID); err != nil {
		t.Fatal(err)
	}
	listenerRemoval := claimTask(t, store, node)
	if listenerRemoval.Kind != "node.listener.apply" || listenerRemoval.NodeListenerState == nil || len(listenerRemoval.NodeListenerState.Listener.Routes) != 0 {
		t.Fatalf("stopping node-direct access did not queue listener removal: %#v", listenerRemoval)
	}
	var gatewayRevisionAfter int64
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision FROM gateway_states WHERE gateway_node_id = ?`, node.ID).Scan(&gatewayRevisionAfter); err != nil {
		t.Fatal(err)
	}
	if gatewayRevisionAfter != gatewayRevisionBefore {
		t.Fatalf("stopping node-direct access changed Site Gateway state: before=%d after=%d", gatewayRevisionBefore, gatewayRevisionAfter)
	}
}

func TestShared443RejectsAnApplicationAlreadyUsing443(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "public-gateway", NodeCapabilities{Gateway: true, Docker: true}, []networking.Candidate{{Address: "10.0.0.20", Interface: "eth0", Kind: networking.KindLAN}, {Address: "203.0.113.20", Interface: "eth0", Kind: networking.KindPublic}}, networking.Profile{ServiceAddress: "10.0.0.20", LANAddress: "10.0.0.20", PublicAddress: "203.0.113.20", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
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
	_, err := store.CreatePublication(ctx, PublicationInput{ServiceID: "vless-443", Kind: publicationShared443, Ingress: PublicationIngress{Owner: ingressApplicationNode}, Hostname: "vless.example.test", SNIHostname: "www.example.test", DNSProvider: "manual"})
	if err == nil || !strings.Contains(err.Error(), "away from port 443") {
		t.Fatalf("shared 443 accepted an occupied local port: %v", err)
	}
}

func TestUninstallRemovesManagedHeadscaleDNS(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	configureBuiltinHeadscaleForTest(t, store)
	node := enrollOrchestrationNode(t, store, "all-in-one", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "100.64.0.40", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.40", HeadscaleAddress: "100.64.0.40", EnabledKinds: []string{networking.KindHeadscale}})
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{Name: "Lab", Code: "lab", Timezone: "UTC", DomainSuffix: "apps.example.test", GatewayNodes: []string{node.ID}}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	installCPA(t, store, node, "100.64.0.40")
	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 2 {
		t.Fatalf("unexpected services: %#v err=%v", services, err)
	}
	if _, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationHeadscale, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID}, Hostname: "cpa.tail.example.test", DNSProvider: "headscale"}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "gateway.routes.apply", nil)
	before, err := os.ReadFile(store.dataDir + "/" + headscaleDNSFile)
	if err != nil || !strings.Contains(string(before), "cpa.tail.example.test") {
		t.Fatalf("managed Headscale record was not created: %s err=%v", before, err)
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Operation: "uninstall", AuthorizedCapabilities: testCapabilityGrant("root")}); err != nil {
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

func TestFailedPublicationCleanupIsPersistedWithoutRetry(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	configureBuiltinHeadscaleForTest(t, store)
	node := enrollOrchestrationNode(t, store, "cleanup-node", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: "100.64.0.41", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.41", HeadscaleAddress: "100.64.0.41", EnabledKinds: []string{networking.KindHeadscale}})
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{Name: "Cleanup", Code: "cleanup", Timezone: "UTC", GatewayNodes: []string{node.ID}}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	installCPA(t, store, node, "100.64.0.41")
	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 2 {
		t.Fatalf("unexpected services: %#v err=%v", services, err)
	}
	publication, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationHeadscale, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID}, Hostname: "retry.tail.example.test", DNSProvider: "headscale"})
	if err != nil {
		t.Fatal(err)
	}

	originalDataDir := store.dataDir
	blocker := t.TempDir() + "/not-a-directory"
	if err := os.WriteFile(blocker, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	store.dataDir = blocker
	if err := store.StopPublication(ctx, publication.ID); err == nil {
		t.Fatal("cleanup failure was hidden")
	}
	var pending, attempt int
	var retryAt string
	if err := store.db.QueryRowContext(ctx, `SELECT cleanup_pending, cleanup_attempt, cleanup_retry_at FROM publications WHERE id = ?`, publication.ID).Scan(&pending, &attempt, &retryAt); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || attempt != 1 || retryAt != "" {
		t.Fatalf("failed cleanup was not scheduled: pending=%d attempt=%d retryAt=%q", pending, attempt, retryAt)
	}

	store.dataDir = originalDataDir
	future := time.Now().UTC().Add(2 * time.Minute)
	store.now = func() time.Time { return future }
	if err := store.cleanupStoppedPublications(ctx, []publicationCleanup{{ID: publication.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT cleanup_pending, cleanup_attempt, cleanup_retry_at FROM publications WHERE id = ?`, publication.ID).Scan(&pending, &attempt, &retryAt); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || attempt != 1 || retryAt != "" {
		t.Fatalf("failed cleanup was retried: pending=%d attempt=%d retryAt=%q", pending, attempt, retryAt)
	}
}

func TestPublicationCanUseGatewayOnAnotherNode(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	worker := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.20.0.11", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.20.0.11", LANAddress: "10.20.0.11", EnabledKinds: []string{networking.KindLAN}})
	gateway := enrollOrchestrationNode(t, store, "gateway", NodeCapabilities{Gateway: true}, []networking.Candidate{{Address: "10.20.0.12", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.20.0.12", LANAddress: "10.20.0.12", EnabledKinds: []string{networking.KindLAN}})
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{Name: "Lab", Code: "lab", Timezone: "UTC", DomainSuffix: "apps.example.test", GatewayNodes: []string{gateway.ID}}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, gateway, "gateway.component.apply", nil)
	installCPA(t, store, worker, "10.20.0.11")
	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 2 {
		t.Fatalf("unexpected services: %#v err=%v", services, err)
	}
	if _, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationLAN, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: gateway.ID}, Hostname: "cpa.apps.example.test", DNSProvider: "manual"}); err != nil {
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
	worker := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.20.0.21", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.20.0.21", LANAddress: "10.20.0.21", EnabledKinds: []string{networking.KindLAN}})
	gateway := enrollOrchestrationNode(t, store, "gateway", NodeCapabilities{Gateway: true}, []networking.Candidate{{Address: "10.20.0.22", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.20.0.22", LANAddress: "10.20.0.22", EnabledKinds: []string{networking.KindLAN}})
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{Name: "Lab", Code: "lab", Timezone: "UTC", DomainSuffix: "apps.example.test", GatewayNodes: []string{gateway.ID}}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, gateway, "gateway.component.apply", nil)
	installCPA(t, store, worker, "10.20.0.21")
	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 2 {
		t.Fatalf("unexpected services: %#v err=%v", services, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE services SET endpoint = '127.0.0.1:8317' WHERE id = ?`, services[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationLAN, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: gateway.ID}, Hostname: "cpa.apps.example.test", DNSProvider: "manual"}); err == nil || !strings.Contains(err.Error(), "routable private service address") {
		t.Fatalf("cross-node loopback origin was accepted: %v", err)
	}
}

func TestPublicationRejectsCrossNodeWithoutSharedPrivateNetwork(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	worker := enrollOrchestrationNode(t, store, "worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "100.64.0.21", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.21", HeadscaleAddress: "100.64.0.21", EnabledKinds: []string{networking.KindHeadscale}})
	gateway := enrollOrchestrationNode(t, store, "gateway", NodeCapabilities{Gateway: true}, []networking.Candidate{{Address: "10.20.0.32", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.20.0.32", LANAddress: "10.20.0.32", EnabledKinds: []string{networking.KindLAN}})
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{Name: "Lab", Code: "lab", Timezone: "UTC", DomainSuffix: "apps.example.test", GatewayNodes: []string{gateway.ID}}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, gateway, "gateway.component.apply", nil)
	installCPA(t, store, worker, "100.64.0.21")
	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 2 {
		t.Fatalf("unexpected services: %#v err=%v", services, err)
	}
	if _, err := store.CreatePublication(ctx, PublicationInput{ServiceID: services[0].ID, Kind: publicationLAN, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: gateway.ID}, Hostname: "cpa.apps.example.test", DNSProvider: "manual"}); err == nil || !strings.Contains(err.Error(), "cannot reach") {
		t.Fatalf("cross-node publication without a shared private network was accepted: %v", err)
	}
}
