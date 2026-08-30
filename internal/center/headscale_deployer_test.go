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
	installer.input = input
	return deployapi.HeadscaleInstallResult{Endpoint: installer.endpoint, APIKey: "hskey-api-abcdefghijklmnopqrstuvwxyz"}, nil
}

func (installer *fakeBuiltinHeadscaleInstaller) ReconcileHeadscale(_ context.Context, input deployapi.HeadscaleInstallRequest) error {
	installer.reconcileInput = input
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
		Headscale: &HeadscaleInput{Mode: "builtin", URL: "https://headscale.example.com"},
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
	integration, err := store.Integration(context.Background(), "headscale")
	if err != nil {
		t.Fatal(err)
	}
	if integration.Mode != "builtin" || integration.Endpoint != headscaleEndpoint || !integration.SecretSet {
		t.Fatalf("unexpected saved integration: %#v", integration)
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
