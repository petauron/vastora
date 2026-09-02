package center

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestCreateDeploymentRequiresConfirmedServiceAddress(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()

	enrollment, err := store.CreateAgentEnrollment(ctx, AgentEnrollmentSpec{
		SiteID:    testSiteID(t, store),
		Name:      "unconfirmed-network",
		CenterURL: "https://center.example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.EnrollAgent(ctx, enrollment.Token, "test", "linux", "amd64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{
		Version:      "test",
		Roles:        []string{"worker"},
		Capabilities: NodeCapabilities{Docker: true},
		NetworkCandidates: []networking.Candidate{{
			Address:   "10.0.0.91",
			Interface: "eth0",
			Kind:      networking.KindLAN,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	_, err = store.CreateDeployment(ctx, DeploymentRequest{
		AgentID: node.ID,
		AppKey:  cpaAppKey,
		Config:  json.RawMessage(`{"debug":false}`),
	})
	if err == nil || !strings.Contains(err.Error(), "confirm the Agent private service address") {
		t.Fatalf("deployment without a confirmed service address was accepted: %v", err)
	}
	var deployments, applications int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployments`).Scan(&deployments); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM applications`).Scan(&applications); err != nil {
		t.Fatal(err)
	}
	if deployments != 0 || applications != 0 {
		t.Fatalf("rejected deployment left partial state: deployments=%d applications=%d", deployments, applications)
	}
}

func TestHeartbeatFiltersVirtualInterfacesAndInvalidatesTheirOldProfile(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "network-filter", NodeCapabilities{Docker: true}, []networking.Candidate{{
		Address: "10.0.0.93", Interface: "eth0", Kind: networking.KindLAN,
	}}, networking.Profile{ServiceAddress: "10.0.0.93", LANAddress: "10.0.0.93", EnabledKinds: []string{networking.KindLAN}})

	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{
		Version: "test", Roles: []string{"worker"}, Capabilities: NodeCapabilities{Docker: true},
		NetworkCandidates: []networking.Candidate{
			{Address: "10.0.0.93", Interface: "docker0", Kind: networking.KindLAN},
			{Address: "10.77.0.6", Interface: "wg0", Kind: networking.KindLAN},
			{Address: "100.64.0.93", Interface: "tailscale0", Kind: networking.KindLAN},
		},
	}); err != nil {
		t.Fatal(err)
	}
	candidates, err := networkCandidates(ctx, store.db, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Interface != "tailscale0" || candidates[0].Kind != networking.KindHeadscale {
		t.Fatalf("Center retained virtual interfaces or trusted the reported kind: %#v", candidates)
	}
	profile, err := networkProfile(ctx, store.db, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if profile != nil {
		t.Fatalf("profile that selected a filtered interface remained confirmed: %#v", profile)
	}
}

func TestListDeploymentsRetainsOldActiveAndReconciliationTasksBeyondRecentLimit(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "deployment-history", NodeCapabilities{Docker: true}, []networking.Candidate{{
		Address: "10.0.0.92", Interface: "eth0", Kind: networking.KindLAN,
	}}, networking.Profile{ServiceAddress: "10.0.0.92", LANAddress: "10.0.0.92", EnabledKinds: []string{networking.KindLAN}})
	siteID := testSiteID(t, store)

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	oldTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	recentTime := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	active := []struct {
		id             string
		appKey         string
		state          string
		reconciliation int
	}{
		{id: "old-pending-deployment", appKey: "test/old-pending", state: "pending"},
		{id: "old-running-deployment", appKey: "test/old-running", state: "running"},
		{id: "old-reconciliation-deployment", appKey: "test/old-reconciliation", state: "failed", reconciliation: 1},
	}
	for _, value := range append(active, struct {
		id             string
		appKey         string
		state          string
		reconciliation int
	}{id: "recent-deployment-app", appKey: "test/recent", state: "succeeded"}) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, status, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, 'running', ?, ?)`, "application-"+value.appKey, value.appKey, node.ID, siteID, value.appKey, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range active {
		if _, err := tx.ExecContext(ctx, `INSERT INTO deployments(id, agent_id, app_key, app_version, manifest_json, config_json, service_address, operation, state, reconciliation_required, error, created_at, updated_at, application_id)
			VALUES(?, ?, ?, '1.0.0', '{}', '{}', '10.0.0.92', 'install', ?, ?, '', ?, ?, ?)`, value.id, node.ID, value.appKey, value.state, value.reconciliation, oldTime, oldTime, "application-"+value.appKey); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 200; index++ {
		createdAt := recentTime.Add(time.Duration(index) * time.Second).Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `INSERT INTO deployments(id, agent_id, app_key, app_version, manifest_json, config_json, service_address, operation, state, error, created_at, updated_at, application_id)
			VALUES(?, ?, 'test/recent', '1.0.0', '{}', '{}', '10.0.0.92', 'install', 'succeeded', '', ?, ?, 'application-test/recent')`, fmt.Sprintf("recent-deployment-%03d", index), node.ID, createdAt, createdAt); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	listed, err := store.ListDeployments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 203 {
		t.Fatalf("listed %d deployments, want 200 recent plus 3 old active/reconciliation tasks", len(listed))
	}
	byID := make(map[string]DeploymentView, len(listed))
	for _, deployment := range listed {
		byID[deployment.ID] = deployment
	}
	for _, value := range active {
		deployment, ok := byID[value.id]
		if !ok {
			t.Fatalf("old %s deployment was omitted after the recent 200: %#v", value.state, listed)
		}
		if deployment.State != value.state || deployment.ReconciliationRequired != (value.reconciliation == 1) {
			t.Fatalf("old deployment state changed while listing: %#v", deployment)
		}
	}
}

func TestReplacingApplicationSecretDeletesTheSupersededSecretRow(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "secret-replacement", NodeCapabilities{Docker: true}, []networking.Candidate{{
		Address: "10.0.0.93", Interface: "eth0", Kind: networking.KindLAN,
	}}, networking.Profile{ServiceAddress: "10.0.0.93", LANAddress: "10.0.0.93", EnabledKinds: []string{networking.KindLAN}})

	created, err := store.CreateDeployment(ctx, DeploymentRequest{
		AgentID: node.ID,
		AppKey:  threeXUIAppKey,
		Role:    threeXUIRoleMaster,
		Config:  json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	completeThreeXUIDeployment(t, store, node, claimTask(t, store, node), "10.0.0.93", "first-api-token")
	var previousSecretID string
	if err := store.db.QueryRowContext(ctx, `SELECT secret_id FROM application_secrets WHERE application_id = ?`, created.ApplicationID).Scan(&previousSecretID); err != nil {
		t.Fatal(err)
	}

	if _, err := store.CreateDeployment(ctx, DeploymentRequest{
		AgentID:   node.ID,
		AppKey:    threeXUIAppKey,
		Operation: "configure",
		Config:    json.RawMessage(`{"enable_fail2ban":false}`),
	}); err != nil {
		t.Fatal(err)
	}
	completeThreeXUIDeployment(t, store, node, claimTask(t, store, node), "10.0.0.93", "second-api-token")

	var currentSecretID string
	if err := store.db.QueryRowContext(ctx, `SELECT secret_id FROM application_secrets WHERE application_id = ?`, created.ApplicationID).Scan(&currentSecretID); err != nil {
		t.Fatal(err)
	}
	if currentSecretID == previousSecretID {
		t.Fatalf("application secret row was not replaced: %q", currentSecretID)
	}
	var oldRows, currentRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM secrets WHERE id = ?`, previousSecretID).Scan(&oldRows); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM secrets WHERE id = ?`, currentSecretID).Scan(&currentRows); err != nil {
		t.Fatal(err)
	}
	if oldRows != 0 || currentRows != 1 {
		t.Fatalf("secret replacement leaked or lost rows: old=%d current=%d", oldRows, currentRows)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	apiToken, secretErr := store.threeXUIAPISecret(ctx, tx, created.ApplicationID)
	_ = tx.Rollback()
	if secretErr != nil || apiToken != "second-api-token" {
		t.Fatalf("replacement secret is not readable: token=%q err=%v", apiToken, secretErr)
	}
}

func TestNetworkEntryAddressChangesAreBlockedByActivePublications(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	oldProfile := networking.Profile{
		ServiceAddress:    "10.0.0.94",
		LANAddress:        "10.0.0.94",
		HeadscaleAddress:  "100.64.0.94",
		PublicAddress:     "203.0.113.94",
		PublicBindAddress: "203.0.113.94",
		PublicMode:        networking.PublicModeDirect,
		EnabledKinds:      []string{networking.KindLAN, networking.KindHeadscale, networking.KindPublic},
		DirectPublic:      true,
	}
	node := enrollOrchestrationNode(t, store, "address-publications", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{
		{Address: "10.0.0.94", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "10.0.0.95", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "100.64.0.94", Interface: "tailscale0", Kind: networking.KindHeadscale},
		{Address: "100.64.0.95", Interface: "tailscale0", Kind: networking.KindHeadscale},
		{Address: "203.0.113.94", Interface: "eth0", Kind: networking.KindPublic},
		{Address: "203.0.113.95", Interface: "eth0", Kind: networking.KindPublic},
	}, oldProfile)
	completeNextTask(t, store, node, "gateway.component.apply", nil)
	applicationID := installCPA(t, store, node, oldProfile.ServiceAddress)
	services, err := store.ListServices(ctx)
	if err != nil || len(services) != 1 || services[0].ApplicationID != applicationID {
		t.Fatalf("services=%#v err=%v", services, err)
	}
	serviceID := services[0].ID
	for _, input := range []PublicationInput{
		{ServiceID: serviceID, Kind: publicationLAN, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID}, Hostname: "cpa.lan.example.test", DNSProvider: "manual"},
		{ServiceID: serviceID, Kind: publicationHeadscale, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID}, Hostname: "cpa.headscale.example.test", DNSProvider: "manual"},
		{ServiceID: serviceID, Kind: publicationPublic, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID}, Hostname: "cpa.public.example.test", DNSProvider: "manual", ConfirmHighRisk: true},
	} {
		if _, err := store.CreatePublication(ctx, input); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name        string
		profile     networking.Profile
		errContains string
	}{
		{
			name: "LAN",
			profile: networking.Profile{ServiceAddress: oldProfile.ServiceAddress, LANAddress: "10.0.0.95", HeadscaleAddress: oldProfile.HeadscaleAddress, PublicAddress: oldProfile.PublicAddress,
				PublicBindAddress: oldProfile.PublicBindAddress, PublicMode: oldProfile.PublicMode, EnabledKinds: oldProfile.EnabledKinds, DirectPublic: true},
			errContains: "stop LAN publications",
		},
		{
			name: "Headscale",
			profile: networking.Profile{ServiceAddress: oldProfile.ServiceAddress, LANAddress: oldProfile.LANAddress, HeadscaleAddress: "100.64.0.95", PublicAddress: oldProfile.PublicAddress,
				PublicBindAddress: oldProfile.PublicBindAddress, PublicMode: oldProfile.PublicMode, EnabledKinds: oldProfile.EnabledKinds, DirectPublic: true},
			errContains: "stop Headscale publications",
		},
		{
			name: "public",
			profile: networking.Profile{ServiceAddress: oldProfile.ServiceAddress, LANAddress: oldProfile.LANAddress, HeadscaleAddress: oldProfile.HeadscaleAddress, PublicAddress: "203.0.113.95",
				PublicBindAddress: "203.0.113.95", PublicMode: oldProfile.PublicMode, EnabledKinds: oldProfile.EnabledKinds, DirectPublic: true},
			errContains: "stop direct public publications",
		},
		{
			name: "public bind",
			profile: networking.Profile{ServiceAddress: oldProfile.ServiceAddress, LANAddress: oldProfile.LANAddress, HeadscaleAddress: oldProfile.HeadscaleAddress, PublicAddress: oldProfile.PublicAddress,
				PublicBindAddress: "10.0.0.95", PublicMode: networking.PublicModeNAT, EnabledKinds: oldProfile.EnabledKinds, DirectPublic: true},
			errContains: "stop direct public publications",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.ConfirmNetworkProfile(ctx, node.ID, test.profile); err == nil || !strings.Contains(err.Error(), test.errContains) {
				t.Fatalf("active publication allowed an entry address change: %v", err)
			}
			current, err := networkProfile(ctx, store.db, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.LANAddress != oldProfile.LANAddress || current.HeadscaleAddress != oldProfile.HeadscaleAddress || current.PublicAddress != oldProfile.PublicAddress {
				t.Fatalf("rejected address change mutated the profile: %#v", current)
			}
		})
	}
}

func TestPublicationChangesAreBlockedDuringApplicationReconciliation(t *testing.T) {
	for _, blocker := range []string{"deployment", "application-command"} {
		t.Run(blocker, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "publication-guard-"+blocker, NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{
				Address: "10.0.0.96", Interface: "eth0", Kind: networking.KindLAN,
			}}, networking.Profile{ServiceAddress: "10.0.0.96", LANAddress: "10.0.0.96", EnabledKinds: []string{networking.KindLAN}})
			completeNextTask(t, store, node, "gateway.component.apply", nil)
			applicationID := installCPA(t, store, node, "10.0.0.96")
			services, err := store.ListServices(ctx)
			if err != nil || len(services) != 1 {
				t.Fatalf("services=%#v err=%v", services, err)
			}
			publication, err := store.CreatePublication(ctx, PublicationInput{
				ServiceID: services[0].ID, Kind: publicationLAN, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID}, Hostname: "cpa.guard.example.test", DNSProvider: "manual",
			})
			if err != nil {
				t.Fatal(err)
			}
			seedApplicationReconciliationBlock(t, store, node.ID, applicationID, blocker)

			_, err = store.CreatePublication(ctx, PublicationInput{
				ServiceID: services[0].ID, Kind: publicationLAN, Ingress: PublicationIngress{Owner: ingressSiteGateway, EntryNodeID: node.ID}, Hostname: "cpa.second.example.test", DNSProvider: "manual",
			})
			if err == nil || !strings.Contains(err.Error(), "recover or finish the application operation") {
				t.Fatalf("publication creation raced %s reconciliation: %v", blocker, err)
			}
			if _, err := store.UpdatePublicationTLS(ctx, publication.ID, true); err == nil || !strings.Contains(err.Error(), "recover or finish the application operation") {
				t.Fatalf("publication TLS change raced %s reconciliation: %v", blocker, err)
			}
			verified, err := store.VerifyPublication(ctx, publication.ID)
			if err != nil {
				t.Fatal(err)
			}
			if verified.Status == "ready" || !strings.Contains(verified.LastError, "recover or finish the application operation") {
				t.Fatalf("verification ignored %s reconciliation: %#v", blocker, verified)
			}
			readyAttempt, err := store.markPublicationReady(ctx, publication.ID, publication.DesiredRevision)
			if err != nil {
				t.Fatal(err)
			}
			if readyAttempt.Status == "ready" || readyAttempt.AppliedRevision == readyAttempt.DesiredRevision {
				t.Fatalf("ready fence ignored %s reconciliation: %#v", blocker, readyAttempt)
			}
		})
	}
}

func TestPublicationReadyCommitIsFencedWhenReconciliationStartsDuringVerification(t *testing.T) {
	for _, blocker := range []string{"deployment", "application-command"} {
		t.Run(blocker, func(t *testing.T) {
			requestStarted := make(chan struct{})
			releaseResponse := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				close(requestStarted)
				<-releaseResponse
				writer.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			store := openOrchestrationStore(t)
			defer store.Close()
			node := seedVerificationPublication(t, store, publicationLAN, "manual", 1, 0, "pending")
			ctx := context.Background()
			endpoint := strings.TrimPrefix(server.URL, "http://")
			_, port, err := net.SplitHostPort(endpoint)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE agent_network_profiles SET lan_address = '127.0.0.1' WHERE agent_id = ?`, node.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE services SET protocol = 'http', endpoint = ? WHERE id = 'verification-service'`, endpoint); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE publications SET hostname = ? WHERE id = 'verification-publication'`, "verification.example.test:"+port); err != nil {
				t.Fatal(err)
			}

			type verificationResult struct {
				publication PublicationView
				err         error
			}
			result := make(chan verificationResult, 1)
			go func() {
				publication, err := store.VerifyPublication(context.Background(), "verification-publication")
				result <- verificationResult{publication: publication, err: err}
			}()
			select {
			case <-requestStarted:
			case <-time.After(time.Second):
				close(releaseResponse)
				t.Fatal("verification did not reach the health check")
			}
			seedApplicationReconciliationBlock(t, store, node.ID, "verification-app", blocker)
			close(releaseResponse)
			verified := <-result
			if verified.err != nil {
				t.Fatal(verified.err)
			}
			if verified.publication.Status == "ready" || verified.publication.AppliedRevision != 0 {
				t.Fatalf("verification committed ready after %s reconciliation started: %#v", blocker, verified.publication)
			}
		})
	}
}

func TestSucceededRealityPublicationRecoveryRequiresReadyGuardAndPreservesExplicitStop(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "reality-publication-recovery", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{
		{Address: "10.0.0.97", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "203.0.113.97", Interface: "eth0", Kind: networking.KindPublic},
	}, networking.Profile{
		ServiceAddress: "10.0.0.97",
		LANAddress:     "10.0.0.97",
		PublicAddress:  "203.0.113.97",
		EnabledKinds:   []string{networking.KindLAN, networking.KindPublic},
		DirectPublic:   true,
	})
	siteID := testSiteID(t, store)
	now := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, image, status, runtime, role, created_at, updated_at)
		VALUES('reality-recovery-app', '3x-ui', ?, ?, ?, '', 'running', 'docker', 'master', ?, ?)`, node.ID, siteID, threeXUIAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, display_name, region_code, protocol, container_port, host_port, endpoint, source, app_protocol, observed_listen, status, created_at, updated_at)
		VALUES('reality-recovery-service', 'reality-recovery-app', ?, 'inbound-9', '🇺🇸 美国Recovery', 'US', 'tcp', 35443, 35443, '10.0.0.97:35443', 'observed', 'vless/tcp/reality', '10.0.0.97', 'ready', ?, ?)`, siteID, now, now); err != nil {
		t.Fatal(err)
	}
	input := RealityCommandTask{
		Action:              "create",
		RegionCode:          "US",
		DisplayName:         "🇺🇸 美国Recovery",
		ConnectHostname:     "reality.recovery.example.test",
		DNSProvider:         "manual",
		TargetHost:          "www.example.com",
		ServerName:          "www.example.com",
		TargetApplicationID: "reality-recovery-app",
		TargetAddress:       "10.0.0.97",
		InboundTag:          "vastora-recovery",
	}
	result := RealityCommandResult{
		Action:          "create",
		InboundID:       9,
		DisplayName:     input.DisplayName,
		Listen:          "10.0.0.97",
		Port:            35443,
		TargetHost:      "www.example.com",
		ServerName:      "www.example.com",
		ConnectHostname: input.ConnectHostname,
	}
	inputJSON, _ := json.Marshal(input)
	resultJSON, _ := json.Marshal(result)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, site_id, display_name, agent_id, gateway_node_id, kind, input_json, result_json, state, created_at, updated_at)
		VALUES('reality-recovery-command', 'reality-recovery-app', ?, ?, ?, ?, ?, ?, ?, 'succeeded', ?, ?)`, siteID, input.DisplayName, node.ID, node.ID, realityCommandKind, inputJSON, resultJSON, now, now); err != nil {
		t.Fatal(err)
	}

	if err := store.resumeSucceededRealityPublications(ctx); err != nil {
		t.Fatal(err)
	}
	store.backgroundWG.Wait()
	publications, err := store.ListPublications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(publications) != 0 {
		t.Fatalf("unguarded REALITY access was reconstructed: %#v", publications)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO three_x_ui_reality_guards(service_id, target_host, target_ip, server_name, node_asn, target_asn, companion_inbound_id, companion_tag, companion_port, status, verified_at, created_at, updated_at)
		VALUES('reality-recovery-service', 'www.example.com', '203.0.113.10', 'www.example.com', 64500, 64500, 10, 'vastora-recovery-guard', 21000, 'ready', ?, ?, ?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	if err := store.resumeSucceededRealityPublications(ctx); err != nil {
		t.Fatal(err)
	}
	store.backgroundWG.Wait()
	publications, err = store.ListPublications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(publications) != 1 || publications[0].ServiceID != "reality-recovery-service" || publications[0].Kind != publicationShared443 || publications[0].Status == "stopped" {
		t.Fatalf("missing REALITY access was not reconstructed: %#v", publications)
	}
	publicationID := publications[0].ID
	if err := store.StopPublication(ctx, publicationID); err != nil {
		t.Fatal(err)
	}

	if err := store.resumeSucceededRealityPublications(ctx); err != nil {
		t.Fatal(err)
	}
	store.backgroundWG.Wait()
	var count int
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(status) FROM publications
		WHERE service_id = 'reality-recovery-service' AND kind = ? AND hostname = ?`, publicationShared443, input.ConnectHostname).Scan(&count, &status); err != nil {
		t.Fatal(err)
	}
	if count != 1 || status != "stopped" {
		t.Fatalf("startup recovery reactivated explicitly stopped REALITY access: count=%d status=%q", count, status)
	}
	var retainedID string
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM publications WHERE service_id = 'reality-recovery-service'`).Scan(&retainedID); err != nil {
		t.Fatal(err)
	}
	if retainedID != publicationID {
		t.Fatalf("startup recovery replaced the user's stopped entry: before=%q after=%q", publicationID, retainedID)
	}
}

func seedApplicationReconciliationBlock(t *testing.T, store *Store, nodeID, applicationID, blocker string) {
	t.Helper()
	ctx := context.Background()
	now := store.now().UTC().Format(time.RFC3339Nano)
	switch blocker {
	case "deployment":
		if _, err := store.db.ExecContext(ctx, `INSERT INTO deployments(id, agent_id, app_key, app_version, manifest_json, config_json, service_address, operation, state, reconciliation_required, error, created_at, updated_at, application_id)
			VALUES('publication-reconciliation-deployment', ?, 'test/publication-reconciliation', '1.0.0', '{}', '{}', '10.0.0.96', 'configure', 'failed', 1, 'state uncertain', ?, ?, ?)`, nodeID, now, now, applicationID); err != nil {
			t.Fatal(err)
		}
	case "application-command":
		if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, agent_id, gateway_node_id, kind, input_json, state, reconciliation_required, error, created_at, updated_at)
			VALUES('publication-reconciliation-command', ?, ?, ?, ?, '{}', 'failed', 1, 'state uncertain', ?, ?)`, applicationID, nodeID, nodeID, clientCommandKind, now, now); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unsupported reconciliation blocker %q", blocker)
	}
}
