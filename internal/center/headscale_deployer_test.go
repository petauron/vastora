package center

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
)

type fakeBuiltinHeadscaleInstaller struct {
	endpoint       string
	input          deployapi.HeadscaleInstallRequest
	reconcileInput deployapi.HeadscaleInstallRequest
	probeInput     deployapi.PublicEntryProbeRequest
	probe          deployapi.PublicEntryProbe
	stoppedProbeID string
	stopProbeErr   error
	remoteAccess   deployapi.CenterRemoteAccessRequest
	installCommits []deployapi.HeadscaleInstallCommitRequest
	installCalls   int
}

type blockingBuiltinHeadscaleInstaller struct {
	fakeBuiltinHeadscaleInstaller
	started chan struct{}
	release chan struct{}
}

func (installer *blockingBuiltinHeadscaleInstaller) ReconcileHeadscale(_ context.Context, input deployapi.HeadscaleInstallRequest) error {
	installer.reconcileInput = input
	close(installer.started)
	<-installer.release
	return nil
}

func (installer *fakeBuiltinHeadscaleInstaller) ApplyCenterRemoteAccess(_ context.Context, input deployapi.CenterRemoteAccessRequest) error {
	installer.remoteAccess = input
	return nil
}

func (installer *fakeBuiltinHeadscaleInstaller) InstallHeadscale(_ context.Context, input deployapi.HeadscaleInstallRequest) (deployapi.HeadscaleInstallResult, error) {
	installer.installCalls++
	installer.input = input
	return deployapi.HeadscaleInstallResult{Endpoint: installer.endpoint, APIKey: "hskey-api-abcdefghijklmnopqrstuvwxyz", APIKeyID: 1, APIKeyPrefix: "abcdefghijkl", APIKeyExpiresAt: time.Now().Add(365 * 24 * time.Hour)}, nil
}

func (installer *fakeBuiltinHeadscaleInstaller) ReconcileHeadscale(_ context.Context, input deployapi.HeadscaleInstallRequest) error {
	installer.reconcileInput = input
	return nil
}

func (installer *fakeBuiltinHeadscaleInstaller) CommitHeadscaleInstall(_ context.Context, input deployapi.HeadscaleInstallCommitRequest) error {
	installer.installCommits = append(installer.installCommits, input)
	return nil
}

func (installer *fakeBuiltinHeadscaleInstaller) StartPublicEntryProbe(_ context.Context, input deployapi.PublicEntryProbeRequest) (deployapi.PublicEntryProbe, error) {
	installer.probeInput = input
	if installer.probe.ID == "" {
		installer.probe = deployapi.PublicEntryProbe{ID: "probe-id", Challenge: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ", Ports: []int{80, 443}, ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339Nano)}
	}
	return installer.probe, nil
}

func (installer *fakeBuiltinHeadscaleInstaller) StopPublicEntryProbe(_ context.Context, id string) error {
	installer.stoppedProbeID = id
	return installer.stopProbeErr
}

func TestSetupStopsPublicProbeWhenExternalVerificationFails(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.discoverNetworkCandidates = func(time.Time) ([]networking.Candidate, error) {
		return []networking.Candidate{{Address: "203.0.113.10", Interface: "eth0", Kind: networking.KindPublic}}, nil
	}
	store.verifyPublicEntry = func(context.Context, string, deployapi.PublicEntryProbe) error {
		return errors.New("probe failed")
	}
	installer := &fakeBuiltinHeadscaleInstaller{}
	server := NewServer(store, "", false).WithInfrastructureManager(installer)
	_, err = server.verifySetupPublicEntry(context.Background(), SetupPublicEntryInput{PublicAddress: "203.0.113.10", GatewayAddress: "203.0.113.10"})
	if err == nil || !strings.Contains(err.Error(), "probe failed") {
		t.Fatalf("unexpected verification error: %v", err)
	}
	if installer.stoppedProbeID != installer.probe.ID {
		t.Fatalf("temporary probe was not stopped: %#v", installer)
	}
	if err := store.requireFreshPublicEntryVerification(context.Background(), setupGatewayBinding{PublicAddress: "203.0.113.10", BindAddress: "203.0.113.10"}); err == nil {
		t.Fatal("failed verification was persisted")
	}
}

func TestSetupVerifiesPublicPortsBeforeInstallingInfrastructure(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.discoverNetworkCandidates = func(time.Time) ([]networking.Candidate, error) {
		return []networking.Candidate{{Address: "10.0.0.157", Interface: "enp0s6", Kind: networking.KindLAN}}, nil
	}
	store.lookupPublicAddress = func(context.Context) (string, error) { return "192.9.143.79", nil }
	store.lookupGatewayAddress = func(string) (string, error) { return "10.0.0.157", nil }
	verifiedAddress := ""
	store.verifyPublicEntry = func(_ context.Context, address string, probe deployapi.PublicEntryProbe) error {
		verifiedAddress = address
		if len(probe.Ports) != 2 || probe.Ports[0] != 80 || probe.Ports[1] != 443 {
			t.Fatalf("unexpected probe: %#v", probe)
		}
		return nil
	}
	installer := &fakeBuiltinHeadscaleInstaller{}
	server := NewServer(store, "", false).WithInfrastructureManager(installer)
	result, err := server.verifySetupPublicEntry(context.Background(), SetupPublicEntryInput{PublicAddress: "192.9.143.79", GatewayAddress: "10.0.0.157"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ready" || result.PublicAddress != "192.9.143.79" || result.GatewayAddress != "10.0.0.157" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if installer.probeInput.BindAddress != "10.0.0.157" || installer.stoppedProbeID != installer.probe.ID || verifiedAddress != "192.9.143.79" {
		t.Fatalf("unexpected probe lifecycle: input=%#v stopped=%q verified=%q", installer.probeInput, installer.stoppedProbeID, verifiedAddress)
	}
	if err := store.requireFreshPublicEntryVerification(context.Background(), setupGatewayBinding{PublicAddress: "192.9.143.79", BindAddress: "10.0.0.157"}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentReportedNATEnablesPublicWebProfileWithoutCenterCoLocation(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	candidates := []networking.Candidate{
		{Address: "10.0.0.27", Interface: "enp0s6", Kind: networking.KindLAN},
		{Address: "100.64.0.1", Interface: "tailscale0", Kind: networking.KindHeadscale},
	}
	node := enrollOrchestrationNode(t, store, "Oracle A1", NodeCapabilities{Docker: true, Gateway: true}, candidates, networking.Profile{
		ServiceAddress: "100.64.0.1", LANAddress: "10.0.0.27", HeadscaleAddress: "100.64.0.1",
		EnabledKinds: []string{networking.KindLAN, networking.KindHeadscale},
	})
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{Version: "test", Roles: []string{"worker", "gateway"}, Capabilities: NodeCapabilities{Docker: true, Gateway: true}, NetworkCandidates: candidates, PublicEgress: &networking.PublicEgress{Address: "198.51.100.27", BindAddress: "10.0.0.27", Mode: networking.PublicModeNAT, ObservedAt: now}, GatewayHealthy: true, ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration, Startup: true}); err != nil {
		t.Fatal(err)
	}
	profile, err := store.ConfirmNetworkProfile(ctx, node.ID, networking.Profile{ServiceAddress: "100.64.0.1", LANAddress: "10.0.0.27", HeadscaleAddress: "100.64.0.1", PublicAddress: "198.51.100.27", PublicBindAddress: "10.0.0.27", PublicMode: networking.PublicModeNAT, EnabledKinds: []string{networking.KindLAN, networking.KindHeadscale, networking.KindPublic}, DirectPublic: true})
	if err != nil {
		t.Fatal(err)
	}
	if profile.PublicMode != networking.PublicModeNAT || profile.PublicAddress != "198.51.100.27" || profile.PublicBindAddress != "10.0.0.27" || !profile.DirectPublic || profile.PublicVerifiedAt != now {
		t.Fatalf("unexpected Agent public profile: %#v", profile)
	}
	if !containsString(profile.EnabledKinds, networking.KindPublic) {
		t.Fatalf("public network was not enabled: %#v", profile.EnabledKinds)
	}
	if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{Version: "test", Roles: []string{"worker", "gateway"}, Capabilities: NodeCapabilities{Docker: true, Gateway: true}, NetworkCandidates: candidates, GatewayHealthy: true, ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration, Startup: true}); err != nil {
		t.Fatalf("startup without an egress observation blocked the heartbeat: %v", err)
	}
	agents, err := store.ListAgents(ctx)
	if err != nil || len(agents) != 1 || agents[0].PublicEgress != nil {
		t.Fatalf("previous-process public egress was not cleared: agents=%#v err=%v", agents, err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	publicAddress, bindAddress, err := validateNodeDirectPublicIngress(ctx, tx, node.ID)
	if err != nil || publicAddress != "198.51.100.27" || bindAddress != "10.0.0.27" {
		t.Fatalf("confirmed NAT mapping was not accepted for node-direct ingress: public=%q bind=%q err=%v", publicAddress, bindAddress, err)
	}
}

func TestSetupInstallsBuiltinHeadscaleWithoutAcceptingAnAPIKey(t *testing.T) {
	headscale := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/user" {
			t.Fatalf("unexpected Headscale verification path: %s", request.URL.Path)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"users": []any{}})
	}))
	defer headscale.Close()
	headscaleEndpoint := fmt.Sprintf("https://example.com:%d", headscale.Listener.Addr().(*net.TCPAddr).Port)
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.headscaleHTTPClient = headscale.Client()
	store.builtinHeadscaleDialAddress = headscale.Listener.Addr().String()
	if _, _, err := store.CreateFirstAdmin(context.Background(), "admin", "correct-horse-battery-staple"); err != nil {
		t.Fatal(err)
	}
	storeSystemCenterCertificateForTest(t, store, "center.example.com")
	if _, err := store.db.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)`, setupGatewayBindingSetting, `{"publicAddress":"192.9.143.79","bindAddress":"10.0.0.157"}`); err != nil {
		t.Fatal(err)
	}
	installer := &fakeBuiltinHeadscaleInstaller{endpoint: headscaleEndpoint}
	server := NewServer(store, "", false).WithInfrastructureManager(installer)
	payload, _ := json.Marshal(InitialSetupInput{
		Site:      SiteInput{Name: "DMIT", Code: "dmit", Timezone: "Asia/Singapore"},
		Network:   CenterNetworkInput{AgentConnectionMode: "headscale", AgentConnectURL: "https://center.example.com"},
		Headscale: &HeadscaleInput{Mode: "builtin", URL: "https://headscale.example.com", DNSPolicy: "custom", DNSResolvers: []string{"9.9.9.9", "149.112.112.112"}},
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/setup/complete", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleSetupComplete(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("setup failed: %d %s", response.Code, response.Body.String())
	}
	if installer.input.CenterURL != "https://center.example.com" || installer.input.HeadscaleURL != "https://headscale.example.com" || installer.input.PublicAddress != "192.9.143.79" || installer.input.GatewayBindAddress != "10.0.0.157" {
		t.Fatalf("unexpected deployment input: %#v", installer.input)
	}
	if installer.input.DNSPolicy != "custom" || len(installer.input.DNSResolvers) != 2 || installer.input.DNSResolvers[0] != "9.9.9.9" {
		t.Fatalf("unexpected Headscale DNS input: %#v", installer.input)
	}
	integration, err := store.Integration(context.Background(), "headscale")
	if err != nil {
		t.Fatal(err)
	}
	if integration.Mode != "builtin" || integration.Endpoint != headscaleEndpoint || !integration.SecretSet || integration.DNSPolicy != "custom" || len(integration.DNSResolvers) != 2 {
		t.Fatalf("unexpected saved integration: %#v", integration)
	}
	keyState, exists, err := store.headscaleAPIKeyState(context.Background())
	if err != nil || !exists || keyState.State != "ready" || keyState.KeyID != 1 || keyState.KeyPrefix != "abcdefghijkl" || keyState.ExpiresAt.IsZero() {
		t.Fatalf("unexpected Headscale API key lifecycle: state=%#v exists=%v err=%v", keyState, exists, err)
	}
	_, runtime, configured, err := store.builtinHeadscaleRuntime(context.Background())
	if err != nil || !configured || runtime != builtinHeadscaleRuntimeVersion {
		t.Fatalf("built-in runtime was not marked current: configured=%v runtime=%q err=%v", configured, runtime, err)
	}
	client, err := store.headscale(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := client.verify(context.Background()); err != nil {
		t.Fatalf("stored built-in Headscale did not use the local gateway: %v", err)
	}
	if _, err := store.ConfigureHeadscale(context.Background(), HeadscaleInput{Mode: "builtin", URL: headscale.URL, APIKey: "user-supplied-key-is-not-accepted"}); err == nil {
		t.Fatal("Store accepted a user-supplied built-in Headscale key")
	}
}

func TestReconcileBuiltinHeadscaleAppliesAnOlderRuntimeOnce(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?), (?, ?)`, agentConnectionModeSetting, "headscale", agentConnectURLSetting, "https://center.example.com"); err != nil {
		t.Fatal(err)
	}
	storeSystemCenterCertificateForTest(t, store, "center.example.com")
	now := store.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if _, err := store.db.ExecContext(ctx, `INSERT INTO network_integrations(kind, mode, endpoint, status, created_at, updated_at)
		VALUES('headscale', 'builtin', 'https://headscale.example.com', 'configured', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	storeBuiltinHeadscaleDNSForTest(t, store, "custom", []string{"1.1.1.1", "1.0.0.1"})
	if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?)`, builtinHeadscaleRuntimeSetting, "ipv4-only-v1"); err != nil {
		t.Fatal(err)
	}
	installer := &fakeBuiltinHeadscaleInstaller{}
	server := NewServer(store, "", false).WithInfrastructureManager(installer)
	if server.startupReady.Load() {
		t.Fatal("Center became ready before built-in Headscale startup reconciliation")
	}
	if err := server.ReconcileBuiltinHeadscale(ctx); err != nil {
		t.Fatal(err)
	}
	if !server.startupReady.Load() {
		t.Fatal("Center did not become ready after built-in Headscale startup reconciliation")
	}
	if installer.reconcileInput.CenterURL != "https://center.example.com" || installer.reconcileInput.HeadscaleURL != "https://headscale.example.com" {
		t.Fatalf("unexpected reconciliation input: %#v", installer.reconcileInput)
	}
	if installer.reconcileInput.CenterPrivateBindAddress != "" {
		t.Fatalf("startup reconciliation trusted a pre-restart tailnet address: %#v", installer.reconcileInput)
	}
	if installer.reconcileInput.DNSPolicy != "custom" || len(installer.reconcileInput.DNSResolvers) != 2 {
		t.Fatalf("startup reconciliation lost Headscale DNS policy: %#v", installer.reconcileInput)
	}
	installer.reconcileInput = deployapi.HeadscaleInstallRequest{}
	if err := server.ReconcileBuiltinHeadscale(ctx); err != nil {
		t.Fatal(err)
	}
	if installer.reconcileInput.CenterURL != "" || installer.reconcileInput.HeadscaleURL != "" {
		t.Fatal("current built-in runtime was reconciled again")
	}
}

func TestReconcileBuiltinHeadscaleSerializesDomainMutations(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?), (?, ?)`, agentConnectionModeSetting, "headscale", agentConnectURLSetting, "https://center.example.com"); err != nil {
		t.Fatal(err)
	}
	storeSystemCenterCertificateForTest(t, store, "center.example.com")
	now := store.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if _, err := store.db.ExecContext(ctx, `INSERT INTO network_integrations(kind, mode, endpoint, status, created_at, updated_at)
		VALUES('headscale', 'builtin', 'https://headscale.example.com', 'configured', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	storeBuiltinHeadscaleDNSForTest(t, store, "system", nil)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?)`, builtinHeadscaleRuntimeSetting, "ipv4-only-v1"); err != nil {
		t.Fatal(err)
	}
	installer := &blockingBuiltinHeadscaleInstaller{started: make(chan struct{}), release: make(chan struct{})}
	server := NewServer(store, "", false).WithInfrastructureManager(installer)
	reconciled := make(chan error, 1)
	go func() {
		reconciled <- server.ReconcileBuiltinHeadscale(ctx)
	}()
	select {
	case <-installer.started:
	case <-time.After(time.Second):
		t.Fatal("Headscale reconciliation did not start")
	}
	lockAcquired := make(chan struct{})
	go func() {
		store.domainSwitchMu.Lock()
		close(lockAcquired)
		store.domainSwitchMu.Unlock()
	}()
	select {
	case <-lockAcquired:
		t.Fatal("domain mutation lock was released while Headscale reconciliation was running")
	case <-time.After(100 * time.Millisecond):
	}
	close(installer.release)
	if err := <-reconciled; err != nil {
		t.Fatal(err)
	}
	select {
	case <-lockAcquired:
	case <-time.After(time.Second):
		t.Fatal("domain mutation lock was not released after Headscale reconciliation")
	}
}

func storeBuiltinHeadscaleDNSForTest(t *testing.T, store *Store, policy string, resolvers []string) {
	t.Helper()
	encoded, err := json.Marshal(resolvers)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO settings(key, value) VALUES(?, ?), (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, headscaleDNSPolicySetting, policy, headscaleDNSResolversSetting, string(encoded)); err != nil {
		t.Fatal(err)
	}
}
