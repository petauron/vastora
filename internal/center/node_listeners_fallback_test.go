package center

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/nodesite"
	"github.com/petauron/vastora/internal/platform"
)

func seedManagedRealityForFallbackTest(t *testing.T, store *Store, node AgentCredential, role, appID, serviceID, pubID, hostname, sni string) {
	t.Helper()
	ctx := context.Background()
	now := store.now().UTC().Format(time.RFC3339Nano)
	siteID := testSiteID(t, store)
	runtimeGeneration := 0
	if role == threeXUIRoleWorker {
		runtimeGeneration = 2
	}
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{
			`INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, runtime_generation, created_at, updated_at)
			 VALUES(?, '3x-ui', ?, ?, 'vastora-official/3x-ui', '', 'running', 'docker', ?, ?, ?, ?)`,
			[]any{appID, node.ID, siteID, role, runtimeGeneration, now, now},
		},
		{
			`INSERT INTO services(id, application_id, site_id, name, display_name, protocol, container_port, host_port, endpoint, source, app_protocol, status, created_at, updated_at)
			 VALUES(?, ?, ?, 'inbound-1', 'Entry', 'tcp', 10443, 10443, '10.0.0.10:10443', 'observed', 'vless/tcp/reality', 'ready', ?, ?)`,
			[]any{serviceID, appID, siteID, now, now},
		},
		{
			`INSERT INTO three_x_ui_reality_guards(service_id, target_host, target_ip, server_name, node_asn, target_asn, companion_inbound_id, companion_tag, companion_port, revision, status, verified_at, created_at, updated_at)
			 VALUES(?, ?, '203.0.113.99', ?, 64500, 64500, 10, 'vastora-test-guard', 21000, 1, 'ready', ?, ?, ?)`,
			[]any{serviceID, sni, sni, now, now, now},
		},
		{
			`INSERT INTO publications(id, service_id, kind, ingress_owner, entry_node_id, hostname, sni_hostname, dns_provider, desired_revision, applied_revision, status, created_at, updated_at)
			 VALUES(?, ?, 'public_shared_443', 'application_node', ?, ?, ?, 'manual', 1, 1, 'ready', ?, ?)`,
			[]any{pubID, serviceID, node.ID, hostname, sni, now, now},
		},
	} {
		if _, err := store.db.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	if role == threeXUIRoleMaster {
		selectTestThreeXUIController(t, store, appID)
	}
}

func readStoredNodeListener(t *testing.T, store *Store, nodeID string) (int64, int64, string, gateway.NodeListenerState) {
	t.Helper()
	var desiredRev, appliedRev int64
	var status string
	var raw []byte
	if err := store.db.QueryRow(`SELECT desired_revision, applied_revision, status, desired_json FROM node_listener_states WHERE node_id = ?`, nodeID).Scan(&desiredRev, &appliedRev, &status, &raw); err != nil {
		t.Fatal(err)
	}
	var state gateway.NodeListenerState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	return desiredRev, appliedRev, status, state
}

func computeDesiredNodeListenerForTest(t *testing.T, store *Store, nodeID string) gateway.NodeListenerState {
	t.Helper()
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	state, err := store.desiredNodeListenerState(context.Background(), tx, nodeID, 1)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func assertRealityRoutePreserved(t *testing.T, listener gateway.SharedHTTPS, bindAddress, sni, expectedAlias string) {
	t.Helper()
	if listener.Address != bindAddress || listener.Port != 443 {
		t.Fatalf("unexpected listener socket %s:%d, want %s:443", listener.Address, listener.Port, bindAddress)
	}
	if len(listener.Routes) != 1 {
		t.Fatalf("expected 1 REALITY route, got %#v", listener.Routes)
	}
	route := listener.Routes[0]
	if !route.ManagedReality || route.Hostname != sni || route.ProxyProtocol != gateway.ProxyProtocolV2 || len(route.Upstreams) != 1 || route.Upstreams[0].Address != expectedAlias || route.Upstreams[0].Port != centerThreeXUIRealityPort {
		t.Fatalf("REALITY route changed unexpectedly: %#v (want alias %q)", route, expectedAlias)
	}
}

func TestNodeListenerRejectsUnmatchedForNonPublicGatewayAndBaseDaylight(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	store.verifyPublication = func(ctx context.Context, id string, _ int64) (PublicationView, error) {
		return store.markPublicationReady(ctx, id, 0)
	}
	configureBuiltinHeadscaleForTest(t, store)
	configureCloudflareZoneForTest(t, store, "example.test")
	store.issuePrivateCertificate = func(_ context.Context, dnsNames ...string) (managedCertificate, error) {
		return testManagedCertificate(t, dnsNames...), nil
	}

	candidates := []networking.Candidate{
		{Address: "10.0.0.11", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "100.64.0.11", Interface: "tailscale0", Kind: networking.KindHeadscale},
		{Address: "203.0.113.11", Interface: "eth0", Kind: networking.KindPublic},
	}
	profile := networking.Profile{
		ServiceAddress:   "10.0.0.11",
		LANAddress:       "10.0.0.11",
		HeadscaleAddress: "100.64.0.11",
		PublicAddress:    "203.0.113.11",
		EnabledKinds:     []string{networking.KindLAN, networking.KindHeadscale, networking.KindPublic},
		DirectPublic:     true,
	}
	node := enrollOrchestrationNode(t, store, "edge-private-only", NodeCapabilities{Docker: true, Gateway: true}, candidates, profile)
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{
		Name: "Edge", Code: "edge", Timezone: "UTC", DomainSuffix: "example.test", GatewayNodes: []string{node.ID},
	}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "gateway.component.apply", nil)

	// Install CPA for non-public gateway routes and seed REALITY shared-443 entry.
	installCPA(t, store, node, "10.0.0.11")
	seedManagedRealityForFallbackTest(t, store, node, threeXUIRoleMaster, "reality-app-1", "reality-svc-1", "reality-pub-1", "entry.example.test", "camo.example.test")

	// Base vastora-official/daylight with tls_loopback_only=0 (table default) only
	// listens on loopback for REALITY camouflage and must never trigger Caddy fallback.
	now := store.now().UTC().Format(time.RFC3339Nano)
	siteID := testSiteID(t, store)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at)
		VALUES('base-daylight', 'Daylight', ?, ?, 'vastora-official/daylight', '', 'running', 'systemd', '', ?, ?)`, node.ID, siteID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO deployments(id, agent_id, app_key, app_version, manifest_json, config_json, operation, state, application_id, created_at, updated_at)
		VALUES('base-daylight-install', ?, 'vastora-official/daylight', '0.1.0', '{}', '{"hostname":"camo.example.test","http_port":24483}', 'install', 'succeeded', 'base-daylight', ?, ?)`, node.ID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO node_sites(application_id, agent_id, hostname, port, revision, applied_revision, state, ca_directory, updated_at, tls_loopback_only)
		VALUES('base-daylight', ?, 'camo.example.test', 24484, 1, 1, 'ready', ?, ?, 0)`, node.ID, certmagic.LetsEncryptProductionCA, now); err != nil {
		t.Fatal(err)
	}

	// Ready Site Gateway + base Daylight alone must reject unmatched SNIs.
	state := computeDesiredNodeListenerForTest(t, store, node.ID)
	assertRealityRoutePreserved(t, state.Listener, "203.0.113.11", "camo.example.test", dockerruntime.ThreeXUIAlias)
	if !state.Listener.RejectUnmatched || state.Listener.CaddyAddress != "" || state.Listener.CaddyPort != 0 {
		t.Fatalf("empty Site Gateway + base Daylight enabled Caddy fallback: %#v", state.Listener)
	}

	// Add applied private LAN TLS, Headscale TLS, and Cloudflare Tunnel gateway routes.
	services, err := store.ListServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var cpaServiceID string
	for _, svc := range services {
		if svc.Name == "api" {
			cpaServiceID = svc.ID
			break
		}
	}
	if cpaServiceID == "" {
		t.Fatalf("CPA api service not found in %#v", services)
	}
	if _, err := store.CreatePublication(ctx, PublicationInput{
		ServiceID:   cpaServiceID,
		Kind:        publicationLAN,
		Ingress:     PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID},
		Hostname:    "lan.edge.example.test",
		DNSProvider: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "gateway.routes.apply", nil)
	if _, err := store.CreatePublication(ctx, PublicationInput{
		ServiceID:   cpaServiceID,
		Kind:        publicationHeadscale,
		Ingress:     PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID},
		Hostname:    "mesh.edge.example.test",
		DNSProvider: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "gateway.routes.apply", nil)

	// Applied private LAN/Headscale TLS must still reject unmatched SNIs on public 443.
	state = computeDesiredNodeListenerForTest(t, store, node.ID)
	assertRealityRoutePreserved(t, state.Listener, "203.0.113.11", "camo.example.test", dockerruntime.ThreeXUIAlias)
	if !state.Listener.RejectUnmatched || state.Listener.CaddyAddress != "" || state.Listener.CaddyPort != 0 {
		t.Fatalf("private LAN/Headscale TLS enabled public Caddy fallback: %#v", state.Listener)
	}

	// An unapplied public_direct HTTPS publication must NOT enable Caddy fallback yet.
	if _, err := store.CreatePublication(ctx, PublicationInput{
		ServiceID:       cpaServiceID,
		Kind:            publicationPublic,
		Ingress:         PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID},
		Hostname:        "pending.example.test",
		DNSProvider:     "manual",
		ConfirmHighRisk: true,
	}); err != nil {
		t.Fatal(err)
	}
	state = computeDesiredNodeListenerForTest(t, store, node.ID)
	if !state.Listener.RejectUnmatched || state.Listener.CaddyAddress != "" || state.Listener.CaddyPort != 0 {
		t.Fatalf("unapplied public_direct route prematurely enabled Caddy fallback: %#v", state.Listener)
	}

	// Standalone REALITY node without Site Gateway role also rejects unmatched SNIs.
	standalone := enrollOrchestrationNode(t, store, "standalone-reality", NodeCapabilities{Docker: true}, []networking.Candidate{
		{Address: "10.0.0.12", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "203.0.113.12", Interface: "eth0", Kind: networking.KindPublic},
	}, networking.Profile{
		ServiceAddress: "10.0.0.12", LANAddress: "10.0.0.12", PublicAddress: "203.0.113.12",
		EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true,
	})
	seedManagedRealityForFallbackTest(t, store, standalone, threeXUIRoleWorker, "reality-app-2", "reality-svc-2", "reality-pub-2", "standalone.example.test", "solo.example.test")
	soloState := computeDesiredNodeListenerForTest(t, store, standalone.ID)
	assertRealityRoutePreserved(t, soloState.Listener, "203.0.113.12", "solo.example.test", dockerruntime.LegacyXrayAlias)
	if !soloState.Listener.RejectUnmatched || soloState.Listener.CaddyAddress != "" || soloState.Listener.CaddyPort != 0 {
		t.Fatalf("standalone REALITY enabled Caddy fallback: %#v", soloState.Listener)
	}
}

func TestNodeListenerPublicHTTPSApplyRemovalOrderingAndVerificationRetry(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()

	var verifyMu sync.Mutex
	verifiedIDs := map[string]int{}
	verifiedCh := make(chan string, 16)
	store.verifyPublication = func(ctx context.Context, id string, _ int64) (PublicationView, error) {
		verifyMu.Lock()
		verifiedIDs[id]++
		verifyMu.Unlock()
		verifiedCh <- id
		return store.markPublicationReady(ctx, id, 0)
	}

	node := enrollOrchestrationNode(t, store, "public-order-edge", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{
		{Address: "10.0.0.21", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "203.0.113.21", Interface: "eth0", Kind: networking.KindPublic},
	}, networking.Profile{
		ServiceAddress: "10.0.0.21", LANAddress: "10.0.0.21", PublicAddress: "203.0.113.21",
		EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true,
	})
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{
		Name: "Order", Code: "order", Timezone: "UTC", DomainSuffix: "example.test", GatewayNodes: []string{node.ID},
	}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	installCPA(t, store, node, "10.0.0.21")
	seedManagedRealityForFallbackTest(t, store, node, threeXUIRoleMaster, "reality-order-app", "reality-order-svc", "reality-order-pub", "entry.example.test", "camo.example.test")

	// Queue and apply initial node listener while no public HTTPS route exists -> reject.
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.queueNodeListenerState(ctx, tx, node.ID, store.now().UTC()); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	firstListener := claimTask(t, store, node)
	if firstListener.Kind != "node.listener.apply" || !firstListener.NodeListenerState.Listener.RejectUnmatched {
		t.Fatalf("initial listener should reject unmatched SNIs: %#v", firstListener)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, firstListener.ID, firstListener.Attempt, true, "", nil, firstListener.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}

	// Publish first public_direct HTTPS site. Before gateway.routes.apply runs,
	// node listener must remain RejectUnmatched=true (no SC window).
	services, err := store.ListServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var apiServiceID string
	for _, svc := range services {
		if svc.Name == "api" {
			apiServiceID = svc.ID
			break
		}
	}
	pub1, err := store.CreatePublication(ctx, PublicationInput{
		ServiceID:       apiServiceID,
		Kind:            publicationPublic,
		Ingress:         PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID},
		Hostname:        "site1.example.test",
		DNSProvider:     "manual",
		ConfirmHighRisk: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an earlier managed DNS failure on public_direct before gateway apply;
	// verify retry is preserved after HAProxy opens Caddy fallback.
	if _, err := store.db.ExecContext(ctx, `UPDATE publications SET dns_provider = 'cloudflare', status = 'failed', last_error = 'Cloudflare DNS unavailable' WHERE id = ?`, pub1.ID); err != nil {
		t.Fatal(err)
	}

	// Next claimed task must be gateway.routes.apply (Caddy opens :443 first).
	gwTask := claimTask(t, store, node)
	if gwTask.Kind != "gateway.routes.apply" {
		t.Fatalf("expected gateway.routes.apply before listener fallback, got %q", gwTask.Kind)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, gwTask.ID, gwTask.Attempt, true, "", nil, gwTask.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	// Publication is NOT ready yet (still failed waiting for DNS retry), yet applied
	// gateway route immediately queued node.listener.apply with Caddy fallback.
	verifyMu.Lock()
	premature := verifiedIDs[pub1.ID]
	verifyMu.Unlock()
	if premature != 0 {
		t.Fatalf("public_direct verification ran before node.listener.apply enabled Caddy fallback: count=%d", premature)
	}

	enableFallback := claimTask(t, store, node)
	if enableFallback.Kind != "node.listener.apply" || enableFallback.NodeListenerState == nil {
		t.Fatalf("expected node.listener.apply after gateway.routes.apply, got %#v", enableFallback)
	}
	if enableFallback.NodeListenerState.Listener.RejectUnmatched || enableFallback.NodeListenerState.Listener.CaddyAddress != dockerruntime.CaddyAlias || enableFallback.NodeListenerState.Listener.CaddyPort != 443 {
		t.Fatalf("node listener did not enable Caddy fallback after applied public HTTPS: %#v", enableFallback.NodeListenerState.Listener)
	}
	assertRealityRoutePreserved(t, enableFallback.NodeListenerState.Listener, "203.0.113.21", "camo.example.test", dockerruntime.ThreeXUIAlias)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, enableFallback.ID, enableFallback.Attempt, true, "", nil, enableFallback.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	// After HAProxy enables fallback, publicationVerificationTargetsForGateway must
	// trigger verification for pub1 even though its status was 'failed'.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-verifiedCh:
			if got == pub1.ID {
				goto verifiedPub1
			}
		case <-deadline:
			t.Fatal("failed public_direct publication was not retried after node.listener.apply enabled Caddy fallback")
		}
	}
verifiedPub1:

	// Add a second public_direct publication; while rev is pending, fallback stays enabled.
	pub2, err := store.CreatePublication(ctx, PublicationInput{
		ServiceID:       apiServiceID,
		Kind:            publicationPublic,
		Ingress:         PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID},
		Hostname:        "site2.example.test",
		DNSProvider:     "manual",
		ConfirmHighRisk: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state := computeDesiredNodeListenerForTest(t, store, node.ID); state.Listener.RejectUnmatched {
		t.Fatalf("adding a second public_direct route flapped Caddy fallback off: %#v", state.Listener)
	}
	completeNextTask(t, store, node, "gateway.routes.apply", nil)

	// Stop pub2 while pub1 still exists -> Caddy fallback stays enabled, no listener change.
	revBeforeStop, _, _, _ := readStoredNodeListener(t, store, node.ID)
	if err := store.StopPublication(ctx, pub2.ID); err != nil {
		t.Fatal(err)
	}
	revAfterStopOne, _, _, _ := readStoredNodeListener(t, store, node.ID)
	if revAfterStopOne != revBeforeStop {
		t.Fatalf("stopping one of two public HTTPS routes unnecessarily re-queued listener: before=%d after=%d", revBeforeStop, revAfterStopOne)
	}
	completeNextTask(t, store, node, "gateway.routes.apply", nil)

	// Stop the LAST public_direct publication -> node.listener.apply (RejectUnmatched=true)
	// must be claimed BEFORE gateway.routes.apply closes Caddy :443.
	if err := store.StopPublication(ctx, pub1.ID); err != nil {
		t.Fatal(err)
	}
	disableFallback := claimTask(t, store, node)
	if disableFallback.Kind != "node.listener.apply" || disableFallback.NodeListenerState == nil {
		t.Fatalf("expected node.listener.apply before gateway route removal, got %#v", disableFallback)
	}
	if !disableFallback.NodeListenerState.Listener.RejectUnmatched || disableFallback.NodeListenerState.Listener.CaddyAddress != "" || disableFallback.NodeListenerState.Listener.CaddyPort != 0 {
		t.Fatalf("stopping last public HTTPS route did not switch HAProxy back to reject: %#v", disableFallback.NodeListenerState.Listener)
	}
	assertRealityRoutePreserved(t, disableFallback.NodeListenerState.Listener, "203.0.113.21", "camo.example.test", dockerruntime.ThreeXUIAlias)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, disableFallback.ID, disableFallback.Attempt, true, "", nil, disableFallback.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "gateway.routes.apply", nil)
}

func TestNodeListenerSystemPublicHTTPSRequiresAppliedPayloadWithoutFlap(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	configureCloudflareZoneForTest(t, store, "example.test")
	store.issuePrivateCertificate = func(_ context.Context, dnsNames ...string) (managedCertificate, error) {
		return testManagedCertificate(t, dnsNames...), nil
	}

	candidates := []networking.Candidate{
		{Address: "10.0.0.31", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "100.64.0.31", Interface: "tailscale0", Kind: networking.KindHeadscale},
		{Address: "203.0.113.31", Interface: "eth0", Kind: networking.KindPublic},
	}
	profile := networking.Profile{
		ServiceAddress: "10.0.0.31", LANAddress: "10.0.0.31", HeadscaleAddress: "100.64.0.31", PublicAddress: "203.0.113.31",
		EnabledKinds: []string{networking.KindLAN, networking.KindHeadscale, networking.KindPublic}, DirectPublic: true,
	}
	node := enrollOrchestrationNode(t, store, "colocated-edge", NodeCapabilities{Docker: true, Gateway: true}, candidates, profile)
	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{
		Name: "CoLocated", Code: "colocated", Timezone: "UTC", DomainSuffix: "example.test", GatewayNodes: []string{node.ID},
	}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	installCPA(t, store, node, "10.0.0.31")
	seedManagedRealityForFallbackTest(t, store, node, threeXUIRoleMaster, "reality-sys-app", "reality-sys-svc", "reality-sys-pub", "entry.example.test", "camo.example.test")

	// Apply a private-only LAN route at gateway rev 1 first, and apply initial reject listener.
	services, err := store.ListServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var apiServiceID string
	for _, svc := range services {
		if svc.Name == "api" {
			apiServiceID = svc.ID
			break
		}
	}
	if _, err := store.CreatePublication(ctx, PublicationInput{
		ServiceID: apiServiceID, Kind: publicationLAN, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID},
		Hostname: "lan.colocated.example.test", DNSProvider: "manual",
	}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "gateway.routes.apply", nil)

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.queueNodeListenerState(ctx, tx, node.ID, store.now().UTC()); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "node.listener.apply", nil)

	// Now enable co-located system public HTTPS routes on 203.0.113.31 and queue gateway rev 2.
	configureBuiltinHeadscaleForTest(t, store)
	storeSystemCenterCertificateForTest(t, store, "center.example.test")
	if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?), (?, ?), (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		agentConnectionModeSetting, "headscale",
		agentConnectURLSetting, "https://center.example.test",
		setupGatewayBindingSetting, `{"publicAddress":"203.0.113.31","bindAddress":"203.0.113.31"}`); err != nil {
		t.Fatal(err)
	}
	if err := store.queueAllGatewayStates(ctx); err != nil {
		t.Fatal(err)
	}

	// Rev 1 only applied private LAN routes; applied_revision=1 must NOT vouch for
	// pending system public HTTPS rev 2 before rev 2 is actually applied.
	if state := computeDesiredNodeListenerForTest(t, store, node.ID); !state.Listener.RejectUnmatched {
		t.Fatalf("private-only applied gateway rev 1 prematurely enabled Caddy fallback for pending system public route: %#v", state.Listener)
	}

	// Applying gateway rev 2 switches HAProxy to Caddy fallback.
	completeNextTask(t, store, node, "gateway.routes.apply", nil)
	listenerTask := claimTask(t, store, node)
	if listenerTask.Kind != "node.listener.apply" || listenerTask.NodeListenerState.Listener.RejectUnmatched || listenerTask.NodeListenerState.Listener.CaddyAddress != dockerruntime.CaddyAlias {
		t.Fatalf("applied system public HTTPS route did not enable Caddy fallback: %#v", listenerTask)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, listenerTask.ID, listenerTask.Attempt, true, "", nil, listenerTask.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}

	// Queue an unrelated gateway rev 3 (pending). Because rev 2 already activated Caddy
	// fallback on 203.0.113.31:443 and system public routes still need it, fallback must not flap.
	if err := store.queueAllGatewayStates(ctx); err != nil {
		t.Fatal(err)
	}
	if state := computeDesiredNodeListenerForTest(t, store, node.ID); state.Listener.RejectUnmatched {
		t.Fatalf("pending follow-up gateway revision flapped active system public HTTPS fallback off: %#v", state.Listener)
	}
}

func TestNodeListenerStandaloneDaylightHTTPSLifecycleAndRollback(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()

	node := enrollOrchestrationNode(t, store, "daylight-edge", NodeCapabilities{Docker: true}, []networking.Candidate{
		{Address: "10.0.0.41", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "203.0.113.41", Interface: "eth0", Kind: networking.KindPublic},
	}, networking.Profile{
		ServiceAddress: "10.0.0.41", LANAddress: "10.0.0.41", PublicAddress: "203.0.113.41",
		EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true,
	})
	seedManagedRealityForFallbackTest(t, store, node, threeXUIRoleMaster, "reality-dl-app", "reality-dl-svc", "reality-dl-pub", "entry.example.test", "camo.example.test")

	// Queue & apply initial reject listener.
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.queueNodeListenerState(ctx, tx, node.ID, store.now().UTC()); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "node.listener.apply", nil)

	// Install standalone vastora-official/daylight-https bound to the same 203.0.113.41:443.
	now := store.now().UTC().Format(time.RFC3339Nano)
	siteID := testSiteID(t, store)
	appID := "daylight-https-app"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at)
		VALUES(?, 'Daylight HTTPS', ?, ?, 'vastora-official/daylight-https', '', 'running', 'systemd', '', ?, ?)`, appID, node.ID, siteID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO deployments(id, agent_id, app_key, app_version, manifest_json, config_json, operation, state, application_id, created_at, updated_at)
		VALUES('dl-https-install', ?, 'vastora-official/daylight-https', '0.1.0', '{}', '{"hostname":"web.example.test","http_port":24481,"https_listen":"203.0.113.41:443"}', 'install', 'succeeded', ?, ?, ?)`, node.ID, appID, now, now); err != nil {
		t.Fatal(err)
	}

	// Stage 1: issuing (no applied TLS yet) -> must stay RejectUnmatched=true.
	if _, err := store.configureNodeSite(ctx, appID, nodeSiteInput{
		Hostname: "web.example.test", Port: 24482, CADirectory: certmagic.LetsEncryptProductionCA,
	}); err != nil {
		t.Fatal(err)
	}
	if state := computeDesiredNodeListenerForTest(t, store, node.ID); !state.Listener.RejectUnmatched {
		t.Fatalf("issuing daylight-https prematurely enabled Caddy fallback: %#v", state.Listener)
	}

	// Stage 2: deliver rev 1 cert -> queues node.listener.apply BEFORE node.site.certificate.
	deliverCert := func(hostname string) {
		t.Helper()
		cert := testManagedCertificate(t, hostname)
		pair, err := tls.X509KeyPair([]byte(cert.CertificatePEM), []byte(cert.PrivateKeyPEM))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.deliverNodeSiteCertificate(ctx, appID, hostname, certmagic.LetsEncryptProductionCA, pair); err != nil {
			t.Fatal(err)
		}
	}
	deliverCert("web.example.test")

	// Verify cancelling a pending rev 1 apply immediately reverts fallback back to reject.
	session, csrf, err := store.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(store, "", false).Handler()
	postNodeSiteAction := func(action string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/applications/"+appID+"/node-site-certificate/actions", strings.NewReader(`{"action":"`+action+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
		req.Header.Set("X-CSRF-Token", csrf)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp
	}
	cancelResp := postNodeSiteAction("cancel")
	if cancelResp.Code != http.StatusAccepted {
		t.Fatalf("cancel action failed: %d %s", cancelResp.Code, cancelResp.Body.String())
	}
	if _, _, _, stored := readStoredNodeListener(t, store, node.ID); !stored.Listener.RejectUnmatched {
		t.Fatalf("cancelling pending initial daylight-https apply did not revert fallback to reject: %#v", stored.Listener)
	}
	completeNextTask(t, store, node, "node.listener.apply", nil)

	applyPublicPort := func(port int) {
		t.Helper()
		loopbackFalse := false
		if _, err := store.configureNodeSite(ctx, appID, nodeSiteInput{
			Hostname: "web.example.test", Port: port, CADirectory: certmagic.LetsEncryptProductionCA, TLSLoopbackOnly: &loopbackFalse,
		}); err != nil {
			t.Fatal(err)
		}
		next := claimTask(t, store, node)
		if next.Kind == "node.listener.apply" {
			if next.NodeListenerState.Listener.RejectUnmatched {
				t.Fatalf("expected Caddy fallback for public daylight-https: %#v", next.NodeListenerState.Listener)
			}
			if err := store.CompleteTask(ctx, node.ID, node.Credential, next.ID, next.Attempt, true, "", nil, next.RequiredRuntimeGeneration); err != nil {
				t.Fatal(err)
			}
			next = claimTask(t, store, node)
		}
		if next.Kind != nodesite.Kind {
			t.Fatalf("expected node.site.certificate, got %q", next.Kind)
		}
		b, _ := pem.Decode([]byte(next.NodeSite.CertificatePEM))
		raw := nodeSiteResultJSON(t, nodesite.Result{
			Action: "apply", Revision: next.Revision, Hostname: "web.example.test",
			State: "applied", Fingerprint: nodesite.Fingerprint(b.Bytes), TLSLoopbackOnly: false,
		})
		if err := store.CompleteTask(ctx, node.ID, node.Credential, next.ID, next.Attempt, true, "", raw, next.RequiredRuntimeGeneration); err != nil {
			t.Fatal(err)
		}
	}
	completeRollback := func() {
		t.Helper()
		resp := postNodeSiteAction("rollback")
		if resp.Code != http.StatusAccepted {
			t.Fatalf("rollback action failed: %d %s", resp.Code, resp.Body.String())
		}
		task := claimTask(t, store, node)
		if task.Kind != nodesite.Kind || task.NodeSite.Action != "rollback" {
			t.Fatalf("expected rollback task, got %#v", task)
		}
		raw := nodeSiteResultJSON(t, nodesite.Result{
			Action: "rollback", Revision: task.Revision, Hostname: "web.example.test", State: "rolled_back",
		})
		if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", raw, task.RequiredRuntimeGeneration); err != nil {
			t.Fatal(err)
		}
	}

	// Stage 3a & 3b: two consecutive apply-from-HTTP-only -> rollback cycles.
	// Neither rollback may keep Caddy fallback from an earlier rolled-back apply.
	for cycle := 1; cycle <= 2; cycle++ {
		applyPublicPort(24482)
		completeRollback()
		afterRollback := claimTask(t, store, node)
		if afterRollback.Kind != "node.listener.apply" || !afterRollback.NodeListenerState.Listener.RejectUnmatched {
			t.Fatalf("cycle %d: rolling back HTTP-only->TLS apply did not switch HAProxy back to reject: %#v", cycle, afterRollback)
		}
		if err := store.CompleteTask(ctx, node.ID, node.Credential, afterRollback.ID, afterRollback.Attempt, true, "", nil, afterRollback.RequiredRuntimeGeneration); err != nil {
			t.Fatal(err)
		}
	}

	// Stage 4: apply two public TLS revisions in sequence, then rollback the second ->
	// prior non-rolled-back applied revision was public TLS, so Caddy fallback stays enabled.
	applyPublicPort(24482)
	applyPublicPort(24485)
	completeRollback()
	if state := computeDesiredNodeListenerForTest(t, store, node.ID); state.Listener.RejectUnmatched {
		t.Fatalf("rolling back to a previous public TLS revision unexpectedly disabled Caddy fallback: %#v", state.Listener)
	}

	// Stage 5: switch to tls_loopback_only=true -> once applied, HAProxy switches to reject.
	loopbackTrue := true
	if _, err := store.configureNodeSite(ctx, appID, nodeSiteInput{
		Hostname: "web.example.test", Port: 24482, CADirectory: certmagic.LetsEncryptProductionCA, TLSLoopbackOnly: &loopbackTrue,
	}); err != nil {
		t.Fatal(err)
	}
	loopbackTask := claimTask(t, store, node)
	if loopbackTask.Kind != nodesite.Kind {
		t.Fatalf("expected node.site.certificate for loopback switch, got %q", loopbackTask.Kind)
	}
	b, _ := pem.Decode([]byte(loopbackTask.NodeSite.CertificatePEM))
	loopbackRaw := nodeSiteResultJSON(t, nodesite.Result{
		Action: "apply", Revision: loopbackTask.Revision, Hostname: "web.example.test",
		State: "applied", Fingerprint: nodesite.Fingerprint(b.Bytes), TLSLoopbackOnly: true,
	})
	if err := store.CompleteTask(ctx, node.ID, node.Credential, loopbackTask.ID, loopbackTask.Attempt, true, "", loopbackRaw, loopbackTask.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	rejectAfterLoopback := claimTask(t, store, node)
	if rejectAfterLoopback.Kind != "node.listener.apply" || !rejectAfterLoopback.NodeListenerState.Listener.RejectUnmatched {
		t.Fatalf("switching daylight-https to tls_loopback_only did not disable Caddy fallback: %#v", rejectAfterLoopback)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, rejectAfterLoopback.ID, rejectAfterLoopback.Attempt, true, "", nil, rejectAfterLoopback.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}

	// Stage 6: wrong https_listen IP or REALITY-owned hostname never enables fallback.
	if _, err := store.db.ExecContext(ctx, `UPDATE node_sites SET tls_loopback_only = 0, state = 'ready', applied_revision = revision WHERE application_id = ?`, appID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET config_json = '{"hostname":"web.example.test","http_port":24481,"https_listen":"198.51.100.99:443"}' WHERE id = 'dl-https-install'`); err != nil {
		t.Fatal(err)
	}
	if state := computeDesiredNodeListenerForTest(t, store, node.ID); !state.Listener.RejectUnmatched {
		t.Fatalf("different https_listen IP enabled Caddy fallback: %#v", state.Listener)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET config_json = '{"hostname":"camo.example.test","http_port":24481,"https_listen":"203.0.113.41:443"}' WHERE id = 'dl-https-install'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE node_sites SET hostname = 'camo.example.test' WHERE application_id = ?`, appID); err != nil {
		t.Fatal(err)
	}
	if state := computeDesiredNodeListenerForTest(t, store, node.ID); !state.Listener.RejectUnmatched {
		t.Fatalf("REALITY-owned hostname enabled Caddy fallback: %#v", state.Listener)
	}
}

func TestNodeListenerRestorePreservesReinstallPreconditions(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()

	node := enrollOrchestrationNode(t, store, "reinstall-edge", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{
		{Address: "10.0.0.51", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "203.0.113.51", Interface: "eth0", Kind: networking.KindPublic},
	}, networking.Profile{
		ServiceAddress: "10.0.0.51", LANAddress: "10.0.0.51", PublicAddress: "203.0.113.51",
		EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true,
	})
	now := store.now().UTC().Format(time.RFC3339Nano)
	siteID := testSiteID(t, store)
	if _, err := store.UpdateSite(ctx, siteID, SiteInput{
		Name: "Restore", Code: "restore", Timezone: "UTC", DomainSuffix: "example.test", GatewayNodes: []string{node.ID},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at)
		VALUES('glivela-restore-app', 'Glivela', ?, ?, ?, '', 'running', 'docker', '', ?, ?)`, node.ID, siteID, glivelaAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, app_protocol, status, created_at, updated_at)
		VALUES('glivela-restore-svc', 'glivela-restore-app', ?, 'entry', 'tcp', 10443, 10443, '10.0.0.51:10443', 'observed', 'meridian/entry', 'ready', ?, ?)`, siteID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO publications(id, service_id, kind, ingress_owner, entry_node_id, hostname, sni_hostname, dns_provider, desired_revision, applied_revision, status, created_at, updated_at)
		VALUES('glivela-restore-pub', 'glivela-restore-svc', 'public_shared_443', 'application_node', ?, 'restore.example.test', 'camo.example.test', 'manual', 1, 1, 'ready', ?, ?)`, node.ID, now, now); err != nil {
		t.Fatal(err)
	}

	restoreTarget := &nodeListenerRestoreTarget{
		ApplicationID:  "glivela-restore-app",
		BackendAddress: "10.0.0.51",
		BindAddress:    "203.0.113.51",
	}
	runRestore := func() (gateway.NodeListenerState, error) {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		return store.buildNodeListenerState(ctx, tx, node.ID, 1, restoreTarget)
	}

	// 1. Existing Site Gateway + running gateway component must still block restore.
	if _, err := runRestore(); err == nil || !strings.Contains(err.Error(), "restore the shared gateway") {
		t.Fatalf("site gateway did not block restore: %v", err)
	}

	// 2. Removing Site Gateway allows restore with RejectUnmatched=true, even with base Daylight.
	if _, err := store.UpdateSite(ctx, siteID, SiteInput{
		Name: "Restore", Code: "restore", Timezone: "UTC", DomainSuffix: "example.test", GatewayNodes: []string{},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at)
		VALUES('restore-base-dl', 'Daylight', ?, ?, 'vastora-official/daylight', '', 'running', 'systemd', '', ?, ?)`, node.ID, siteID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO deployments(id, agent_id, app_key, app_version, manifest_json, config_json, operation, state, application_id, created_at, updated_at)
		VALUES('restore-base-dl-inst', ?, 'vastora-official/daylight', '0.1.0', '{}', '{"hostname":"camo.example.test","http_port":24483}', 'install', 'succeeded', 'restore-base-dl', ?, ?)`, node.ID, now, now); err != nil {
		t.Fatal(err)
	}
	restored, err := runRestore()
	if err != nil {
		t.Fatalf("restore failed on non-gateway node with base Daylight: %v", err)
	}
	if !restored.Listener.RejectUnmatched || restored.Listener.CaddyAddress != "" || restored.Listener.CaddyPort != 0 {
		t.Fatalf("restored listener unexpectedly enabled Caddy fallback: %#v", restored.Listener)
	}

	// 3. Active standalone daylight-https public TLS must also block restore fail-closed.
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at)
		VALUES('restore-dl-https', 'Daylight HTTPS', ?, ?, 'vastora-official/daylight-https', '', 'running', 'systemd', '', ?, ?)`, node.ID, siteID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO deployments(id, agent_id, app_key, app_version, manifest_json, config_json, operation, state, application_id, created_at, updated_at)
		VALUES('restore-dl-https-inst', ?, 'vastora-official/daylight-https', '0.1.0', '{}', '{"hostname":"web.example.test","http_port":24481,"https_listen":"203.0.113.51:443"}', 'install', 'succeeded', 'restore-dl-https', ?, ?)`, node.ID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO node_sites(application_id, agent_id, hostname, port, revision, applied_revision, state, ca_directory, updated_at, tls_loopback_only)
		VALUES('restore-dl-https', ?, 'web.example.test', 24482, 1, 1, 'ready', ?, ?, 0)`, node.ID, certmagic.LetsEncryptProductionCA, now); err != nil {
		t.Fatal(err)
	}
	if _, err := runRestore(); err == nil || !strings.Contains(err.Error(), "restore the shared gateway") {
		t.Fatalf("standalone public daylight-https did not block restore: %v", err)
	}
}

func TestReconcileNodeListenerSingleNodeCanaryIdempotenceAndGuards(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	store.verifyPublication = func(ctx context.Context, id string, _ int64) (PublicationView, error) {
		return store.markPublicationReady(ctx, id, 0)
	}

	enrollEdge := func(name, lanIP, pubIP string) (AgentCredential, []networking.Candidate) {
		t.Helper()
		cands := []networking.Candidate{
			{Address: lanIP, Interface: "eth0", Kind: networking.KindLAN},
			{Address: pubIP, Interface: "eth0", Kind: networking.KindPublic},
		}
		node := enrollOrchestrationNode(t, store, name, NodeCapabilities{Docker: true, Gateway: true}, cands, networking.Profile{
			ServiceAddress: lanIP, LANAddress: lanIP, PublicAddress: pubIP,
			EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true,
		})
		return node, cands
	}
	legacyA, candsA := enrollEdge("edge-a", "10.0.0.61", "203.0.113.61")
	legacyB, candsB := enrollEdge("edge-b", "10.0.0.62", "203.0.113.62")
	realPublic, _ := enrollEdge("public-edge", "10.0.0.63", "203.0.113.63")
	noListener, _ := enrollEdge("plain-edge", "10.0.0.64", "203.0.113.64")

	if _, err := store.UpdateSite(ctx, testSiteID(t, store), SiteInput{
		Name: "Canary", Code: "canary", Timezone: "UTC", DomainSuffix: "example.test",
		GatewayNodes: []string{legacyA.ID, legacyB.ID, realPublic.ID},
	}); err != nil {
		t.Fatal(err)
	}
	for _, node := range []AgentCredential{legacyA, legacyB, realPublic} {
		completeNextTask(t, store, node, "gateway.component.apply", nil)
	}

	// Seed REALITY on legacyA (controller), legacyB (worker), and realPublic (worker).
	seedManagedRealityForFallbackTest(t, store, legacyA, threeXUIRoleMaster, "app-a", "svc-a", "pub-a", "a.example.test", "camo-a.example.test")
	seedManagedRealityForFallbackTest(t, store, legacyB, threeXUIRoleWorker, "app-b", "svc-b", "pub-b", "b.example.test", "camo-b.example.test")
	seedManagedRealityForFallbackTest(t, store, realPublic, threeXUIRoleWorker, "app-p", "svc-p", "pub-p", "p.example.test", "camo-p.example.test")

	// Publish a genuine public_direct HTTPS site on realPublic only.
	installCPA(t, store, realPublic, "10.0.0.63")
	services, err := store.ListServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var pubCPAService string
	for _, svc := range services {
		if svc.Name == "api" {
			pubCPAService = svc.ID
			break
		}
	}
	if _, err := store.CreatePublication(ctx, PublicationInput{
		ServiceID: pubCPAService, Kind: publicationPublic, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: realPublic.ID},
		Hostname: "real.example.test", DNSProvider: "manual", ConfirmHighRisk: true,
	}); err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, realPublic, "gateway.routes.apply", nil)

	// Persist legacy buggy ready listener state (RejectUnmatched=false) on legacyA and legacyB,
	// and genuine Caddy fallback ready listener state on realPublic.
	now := store.now().UTC().Format(time.RFC3339Nano)
	seedLegacyFallback := func(node AgentCredential, pubIP, sni, pubID, alias string) string {
		t.Helper()
		state := gateway.NodeListenerState{
			Revision: 1,
			NodeID:   node.ID,
			Listener: gateway.SharedHTTPS{
				Address:         pubIP,
				Port:            443,
				CaddyAddress:    dockerruntime.CaddyAlias,
				CaddyPort:       443,
				RejectUnmatched: false,
				Routes: []gateway.Layer4Route{{
					ID:                pubID,
					Hostname:          sni,
					ApplicationNodeID: node.ID,
					ManagedReality:    true,
					ProxyProtocol:     gateway.ProxyProtocolV2,
					Upstreams:         []gateway.Upstream{{Address: alias, Port: centerThreeXUIRealityPort}},
				}},
			},
		}.Sorted()
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `INSERT INTO node_listener_states(node_id, desired_revision, applied_revision, desired_json, status, updated_at)
			VALUES(?, 1, 1, ?, 'ready', ?)
			ON CONFLICT(node_id) DO UPDATE SET desired_revision=1, applied_revision=1, desired_json=excluded.desired_json, status='ready', updated_at=excluded.updated_at`, node.ID, raw, now); err != nil {
			t.Fatal(err)
		}
		hash, err := gateway.NodeListenerConfigurationHash(state)
		if err != nil {
			t.Fatal(err)
		}
		return hash
	}
	hashA := seedLegacyFallback(legacyA, "203.0.113.61", "camo-a.example.test", "pub-a", dockerruntime.ThreeXUIAlias)
	hashB := seedLegacyFallback(legacyB, "203.0.113.62", "camo-b.example.test", "pub-b", dockerruntime.LegacyXrayAlias)
	seedLegacyFallback(realPublic, "203.0.113.63", "camo-p.example.test", "pub-p", dockerruntime.LegacyXrayAlias)

	// Center startup and routine matching heartbeats must NOT fan out HAProxy rebuilds.
	if err := store.StartPublicationVerifications(ctx); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		node  AgentCredential
		cands []networking.Candidate
		pubIP string
		hash  string
	}{
		{legacyA, candsA, "203.0.113.61", hashA},
		{legacyB, candsB, "203.0.113.62", hashB},
	} {
		if err := store.RecordAgentHeartbeat(ctx, entry.node.ID, entry.node.Credential, NodeHeartbeat{
			Version:                      "test",
			Roles:                        []string{"worker", "gateway"},
			Capabilities:                 testRuntimeCapabilities(NodeCapabilities{Docker: true, Gateway: true}),
			NetworkCandidates:            entry.cands,
			PublicEgress:                 &networking.PublicEgress{Address: entry.pubIP, BindAddress: entry.pubIP, Mode: networking.PublicModeDirect, ObservedAt: store.now().UTC()},
			GatewayHealthy:               true,
			NodeListenerHealthy:          true,
			NodeListenerRevision:         1,
			NodeListenerConfigHash:       entry.hash,
			ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, node := range []AgentCredential{legacyA, legacyB, realPublic} {
		rev, _, status, _ := readStoredNodeListener(t, store, node.ID)
		if rev != 1 || status != "ready" {
			t.Fatalf("startup/heartbeat unexpectedly mutated listener on %s: rev=%d status=%q", node.ID, rev, status)
		}
	}

	// HTTP endpoint enforces authentication and CSRF.
	session, csrf, err := store.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(store, "", false).Handler()
	postReconcile := func(agentID string, withSession, withCSRF bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agentID+"/node-listener/reconcile", nil)
		if withSession {
			req.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
		}
		if withCSRF {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp
	}
	if resp := postReconcile(legacyA.ID, false, false); resp.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated reconcile returned %d", resp.Code)
	}
	if resp := postReconcile(legacyA.ID, true, false); resp.Code != http.StatusUnauthorized {
		t.Fatalf("reconcile without CSRF returned %d", resp.Code)
	}

	// Canary reconcile on legacyA queues revision 2 with RejectUnmatched=true ONLY for legacyA.
	var gwRevABefore int64
	_ = store.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT desired_revision FROM gateway_states WHERE gateway_node_id = ?), 0)`, legacyA.ID).Scan(&gwRevABefore)

	resp := postReconcile(legacyA.ID, true, true)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("canary reconcile on legacyA returned %d: %s", resp.Code, resp.Body.String())
	}
	var result NodeListenerReconcileResult
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Queued || result.Revision != 2 || result.Status != "pending" || !result.RejectUnmatched {
		t.Fatalf("unexpected canary reconcile result: %#v", result)
	}

	// Verify legacyB, realPublic, and legacyA's gateway_states were untouched.
	var gwRevAAfter int64
	_ = store.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT desired_revision FROM gateway_states WHERE gateway_node_id = ?), 0)`, legacyA.ID).Scan(&gwRevAAfter)
	if gwRevAAfter != gwRevABefore {
		t.Fatalf("node-listener reconcile touched gateway_states: before=%d after=%d", gwRevABefore, gwRevAAfter)
	}
	if revB, _, statusB, stateB := readStoredNodeListener(t, store, legacyB.ID); revB != 1 || statusB != "ready" || stateB.Listener.RejectUnmatched {
		t.Fatalf("canary reconcile on legacyA affected legacyB: rev=%d status=%q listener=%#v", revB, statusB, stateB.Listener)
	}
	if revP, _, statusP, stateP := readStoredNodeListener(t, store, realPublic.ID); revP != 1 || statusP != "ready" || stateP.Listener.RejectUnmatched {
		t.Fatalf("canary reconcile on legacyA affected realPublic: rev=%d status=%q listener=%#v", revP, statusP, stateP.Listener)
	}

	// Repeating reconcile while revision 2 is pending is an idempotent 200 no-op.
	repeatPending := postReconcile(legacyA.ID, true, true)
	if repeatPending.Code != http.StatusOK {
		t.Fatalf("repeat pending reconcile returned %d: %s", repeatPending.Code, repeatPending.Body.String())
	}
	if err := json.Unmarshal(repeatPending.Body.Bytes(), &result); err != nil || result.Queued || result.Revision != 2 || result.Status != "pending" {
		t.Fatalf("unexpected repeat pending result: %#v err=%v", result, err)
	}

	// Claim and complete legacyA's listener update; verify REALITY route preserved.
	taskA := claimTask(t, store, legacyA)
	if taskA.Kind != "node.listener.apply" || !taskA.NodeListenerState.Listener.RejectUnmatched || taskA.NodeListenerState.Listener.CaddyAddress != "" || taskA.NodeListenerState.Listener.CaddyPort != 0 {
		t.Fatalf("unexpected legacyA reconcile task: %#v", taskA)
	}
	assertRealityRoutePreserved(t, taskA.NodeListenerState.Listener, "203.0.113.61", "camo-a.example.test", dockerruntime.ThreeXUIAlias)
	if err := store.CompleteTask(ctx, legacyA.ID, legacyA.Credential, taskA.ID, taskA.Attempt, true, "", nil, taskA.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}

	// Repeating reconcile after convergence and on unchanged realPublic are 200 no-ops.
	repeatReady := postReconcile(legacyA.ID, true, true)
	if repeatReady.Code != http.StatusOK {
		t.Fatalf("repeat ready reconcile returned %d: %s", repeatReady.Code, repeatReady.Body.String())
	}
	if err := json.Unmarshal(repeatReady.Body.Bytes(), &result); err != nil || result.Queued || result.Revision != 2 || result.Status != "ready" || !result.RejectUnmatched {
		t.Fatalf("unexpected repeat ready result: %#v err=%v", result, err)
	}
	unchangedPublic := postReconcile(realPublic.ID, true, true)
	if unchangedPublic.Code != http.StatusOK {
		t.Fatalf("reconcile on unchanged realPublic returned %d: %s", unchangedPublic.Code, unchangedPublic.Body.String())
	}
	if err := json.Unmarshal(unchangedPublic.Body.Bytes(), &result); err != nil || result.Queued || result.Revision != 1 || result.Status != "ready" || result.RejectUnmatched {
		t.Fatalf("realPublic listener unexpectedly changed: %#v err=%v", result, err)
	}

	// Guard checks: 404 when node has no managed listener; 409 when failed, busy, or execution-blocked.
	if resp := postReconcile(noListener.ID, true, true); resp.Code != http.StatusNotFound {
		t.Fatalf("no-listener node returned %d, want 404", resp.Code)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE node_listener_states SET status = 'failed' WHERE node_id = ?`, legacyB.ID); err != nil {
		t.Fatal(err)
	}
	if resp := postReconcile(legacyB.ID, true, true); resp.Code != http.StatusConflict {
		t.Fatalf("failed listener returned %d, want 409", resp.Code)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE node_listener_states SET status = 'applying' WHERE node_id = ?`, legacyB.ID); err != nil {
		t.Fatal(err)
	}
	if resp := postReconcile(legacyB.ID, true, true); resp.Code != http.StatusConflict {
		t.Fatalf("busy listener returned %d, want 409", resp.Code)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE node_listener_states SET status = 'ready' WHERE node_id = ?`, legacyB.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO task_executions(id, agent_id, task_id, kind, attempt, session_id, digest, sealed_task, state, phase, expires_at, created_at, updated_at)
		VALUES('exec-fence-b', ?, 'gateway-route-b-r9', 'gateway.routes.apply', 1, 'sess-b', 'digest-b', X'00', 'unknown', 'started', ?, ?, ?)`, legacyB.ID, now, now, now); err != nil {
		t.Fatal(err)
	}
	if resp := postReconcile(legacyB.ID, true, true); resp.Code != http.StatusConflict {
		t.Fatalf("execution-fenced node returned %d, want 409", resp.Code)
	}
}
