package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/gatewayruntime"
)

type fakeGatewayDriver struct {
	mu            sync.Mutex
	applied       []gateway.DesiredState
	certificates  []gateway.Certificate
	fail          bool
	healthFail    bool
	blockRevision int64
	applyStarted  chan struct{}
	releaseApply  chan struct{}
	startOnce     sync.Once
}

type fakeLayer4Provisioner struct {
	applyErr error
	removed  int
}

type fakeGatewayRuntimeProvisioner struct {
	states []gateway.DesiredState
}

func (provisioner *fakeGatewayRuntimeProvisioner) Reconcile(_ context.Context, state gateway.DesiredState) error {
	provisioner.states = append(provisioner.states, state.Sorted())
	return nil
}

type staticSystemGatewayInspector []string

func (services staticSystemGatewayInspector) ProtectedSystemServices(context.Context) ([]string, error) {
	return append([]string(nil), services...), nil
}

func (provisioner *fakeLayer4Provisioner) Apply(context.Context, gateway.SharedHTTPS) error {
	return provisioner.applyErr
}

func (provisioner *fakeLayer4Provisioner) Remove(context.Context) error {
	provisioner.removed++
	return nil
}

func (*fakeLayer4Provisioner) Health(context.Context) error { return nil }

func (driver *fakeGatewayDriver) ApplyConfiguration(_ context.Context, desired gateway.DesiredState, certificates []gateway.Certificate) error {
	if desired.Revision == driver.blockRevision && driver.releaseApply != nil {
		if driver.applyStarted != nil {
			driver.startOnce.Do(func() { close(driver.applyStarted) })
		}
		<-driver.releaseApply
	}
	if driver.fail {
		return errors.New("Caddy rejected configuration")
	}
	driver.mu.Lock()
	driver.applied = append(driver.applied, desired.Sorted())
	driver.certificates = append([]gateway.Certificate(nil), certificates...)
	driver.mu.Unlock()
	return nil
}

func (driver *fakeGatewayDriver) CurrentConfiguration() (gateway.DesiredState, []gateway.Certificate) {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if len(driver.applied) == 0 {
		return gateway.DesiredState{}, nil
	}
	return driver.applied[len(driver.applied)-1].Sorted(), append([]gateway.Certificate(nil), driver.certificates...)
}
func (driver *fakeGatewayDriver) ApplyRoute(context.Context, gateway.Route) error { return nil }
func (driver *fakeGatewayDriver) DeleteRoute(context.Context, string) error       { return nil }
func (driver *fakeGatewayDriver) ListRoutes(context.Context) ([]gateway.Route, error) {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	if len(driver.applied) == 0 {
		return nil, nil
	}
	return driver.applied[len(driver.applied)-1].Routes, nil
}

func (driver *fakeGatewayDriver) appliedStates() []gateway.DesiredState {
	driver.mu.Lock()
	defer driver.mu.Unlock()
	return append([]gateway.DesiredState(nil), driver.applied...)
}
func (driver *fakeGatewayDriver) GetRouteStatus(context.Context, string) (string, error) {
	return "ready", nil
}
func (driver *fakeGatewayDriver) Health(context.Context) error {
	if driver.healthFail {
		return errors.New("Caddy health check failed")
	}
	return nil
}

func gatewayState(revision int64, port int) gateway.DesiredState {
	return gateway.DesiredState{
		Revision:  revision,
		Listeners: []gateway.Listener{{Kind: "headscale", Address: "100.64.0.2", HTTPPort: 80, HTTPSPort: 443}},
		Routes:    []gateway.Route{{ID: "cpa-route", Hostname: "cpa.apps.example.test", Protocol: "http", ListenerKind: "headscale", Upstreams: []gateway.Upstream{{Address: "100.64.0.10", Port: port}}}},
	}
}

func testGatewayCertificate(t *testing.T, hostname string) gateway.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
	}, &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
	}, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return gateway.Certificate{
		Hostname:       hostname,
		CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		PrivateKeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
	}
}

func TestGatewayHealthFailureDoesNotAdvanceLastKnownGood(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	driver := &fakeGatewayDriver{}
	if err := applyGatewayDesiredState(ctx, store, driver, gatewayState(1, 3000), nil); err != nil {
		t.Fatal(err)
	}
	driver.healthFail = true
	if err := applyGatewayDesiredState(ctx, store, driver, gatewayState(2, 3100), nil); err == nil {
		t.Fatal("unhealthy Caddy revision was accepted")
	}
	persisted, err := store.GatewayState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Desired.Revision != 1 {
		t.Fatalf("health failure advanced last-known-good state: %#v", persisted)
	}
}

func TestGatewayRevisionIsIdempotentAndFailureKeepsLastKnownGood(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	driver := &fakeGatewayDriver{}
	if err := applyGatewayDesiredState(ctx, store, driver, gatewayState(1, 3000), nil); err != nil {
		t.Fatal(err)
	}
	if err := applyGatewayDesiredState(ctx, store, driver, gatewayState(1, 3000), nil); err != nil {
		t.Fatal(err)
	}
	if len(driver.applied) != 1 {
		t.Fatalf("duplicate revision was applied %d times", len(driver.applied))
	}
	driver.fail = true
	if err := applyGatewayDesiredState(ctx, store, driver, gatewayState(2, 3100), nil); err == nil {
		t.Fatal("failed Caddy load was accepted")
	}
	persisted, err := store.GatewayState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Desired.Revision != 1 || persisted.Desired.Routes[0].Upstreams[0].Port != 3000 {
		t.Fatalf("last-known-good state changed after failure: %#v", persisted)
	}
}

func TestGatewayRestoresAfterAgentOrCaddyRestartWithoutCenter(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	initial := &fakeGatewayDriver{}
	if err := applyGatewayDesiredState(ctx, store, initial, gatewayState(7, 3000), nil); err != nil {
		t.Fatal(err)
	}
	restarted := &fakeGatewayDriver{}
	if err := restoreGatewayState(ctx, store, restarted); err != nil {
		t.Fatal(err)
	}
	if len(restarted.applied) != 1 || restarted.applied[0].Revision != 7 {
		t.Fatalf("restart did not restore persisted state: %#v", restarted.applied)
	}
}

func TestGatewayStartupWithoutAppliedStateDoesNotRequireRuntime(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	driver := &fakeGatewayDriver{healthFail: true}
	if err := restoreGatewayState(context.Background(), store, driver); err != nil {
		t.Fatalf("unused gateway capability required a running gateway: %v", err)
	}
	if err := store.requireGatewayStartup(); err != nil {
		t.Fatalf("unused gateway capability blocked the control plane: %v", err)
	}
}

func TestGatewayStartupPreservesHealthyProtectedSystemGatewayWhenPersistedStateIsStale(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.RecordGatewayState(context.Background(), gatewayState(29, 3000), nil); err != nil {
		t.Fatal(err)
	}
	admin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/config/" {
			t.Fatalf("unexpected Caddy request: %s %s", request.Method, request.URL.Path)
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer admin.Close()
	driver, err := NewCaddyGatewayDriver(admin.URL)
	if err != nil {
		t.Fatal(err)
	}
	driver.SystemGateway = staticSystemGatewayInspector{"center", "headscale"}
	if err := restoreGatewayState(context.Background(), store, driver); err != nil {
		t.Fatalf("healthy protected gateway was not preserved: %v", err)
	}
	if err := store.requireGatewayStartup(); err != nil {
		t.Fatalf("stale persisted state blocked Center reconciliation: %v", err)
	}
}

func TestGatewayStartupFailsClosedWhenProtectedSystemGatewayCannotBeVerified(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.RecordGatewayState(context.Background(), gatewayState(29, 3000), nil); err != nil {
		t.Fatal(err)
	}
	admin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer admin.Close()
	driver, err := NewCaddyGatewayDriver(admin.URL)
	if err != nil {
		t.Fatal(err)
	}
	driver.SystemGateway = staticSystemGatewayInspector{"center", "headscale"}
	if err := restoreGatewayState(context.Background(), store, driver); err == nil || !strings.Contains(err.Error(), "verify preserved protected system gateway") {
		t.Fatalf("unhealthy protected gateway did not fail closed: %v", err)
	}
	if err := store.requireGatewayStartup(); err == nil {
		t.Fatal("failed protected gateway verification did not fence the control plane")
	}
}

func TestGatewayStartupRestoreFencesNewerRevisionUntilRestoreFinishes(t *testing.T) {
	assertGatewayStartupFence(t, gatewayState(7, 3000), nil, gatewayState(8, 3100), nil)
}

func TestGatewayStartupFenceCoversRouteCertificateSharedHTTPSAndPortMutations(t *testing.T) {
	base := gatewayState(7, 3000)
	updated := gatewayState(8, 3100)
	added := gatewayState(8, 3000)
	added.Routes = append(added.Routes, gateway.Route{ID: "added-route", Hostname: "added.apps.example.test", Protocol: "http", ListenerKind: "headscale", Upstreams: []gateway.Upstream{{Address: "100.64.0.11", Port: 3200}}})
	stopped := gatewayState(8, 3000)
	stopped.Routes = nil
	portChanged := gatewayState(8, 3000)
	portChanged.Listeners[0].HTTPPort = 10080
	portChanged.Listeners[0].HTTPSPort = 10443
	tlsBase := gatewayState(7, 3000)
	tlsBase.Routes[0].TLSEnabled = true
	tlsNext := gatewayState(8, 3000)
	tlsNext.Routes[0].TLSEnabled = true
	oldCertificate := testGatewayCertificate(t, tlsBase.Routes[0].Hostname)
	newCertificate := testGatewayCertificate(t, tlsNext.Routes[0].Hostname)
	publicBase := gateway.DesiredState{
		Revision:  7,
		Listeners: []gateway.Listener{{Kind: "public", Address: "203.0.113.10", HTTPPort: 80, HTTPSPort: 443}},
		Routes:    []gateway.Route{{ID: "web-route", Hostname: "web.example.test", Protocol: "https", TLSEnabled: true, ListenerKind: "public", Upstreams: []gateway.Upstream{{Address: "100.64.0.10", Port: 3000}}}},
	}
	sharedEnabled := publicBase
	sharedEnabled.Revision = 8
	sharedEnabled.SharedHTTPS = &gateway.SharedHTTPS{Address: "203.0.113.10", Port: 443, CaddyAddress: "127.0.0.1", CaddyPort: 10443, Routes: []gateway.Layer4Route{{ID: "raw-route", Hostname: "raw.example.test", Upstreams: []gateway.Upstream{{Address: "100.64.0.12", Port: 443}}}}}

	for _, test := range []struct {
		name             string
		initial, desired gateway.DesiredState
		initialCerts     []gateway.Certificate
		desiredCerts     []gateway.Certificate
	}{
		{name: "update route", initial: base, desired: updated},
		{name: "add route", initial: base, desired: added},
		{name: "stop route", initial: base, desired: stopped},
		{name: "rotate certificate", initial: tlsBase, desired: tlsNext, initialCerts: []gateway.Certificate{oldCertificate}, desiredCerts: []gateway.Certificate{newCertificate}},
		{name: "enable shared 443", initial: publicBase, desired: sharedEnabled},
		{name: "change Caddy ports", initial: base, desired: portChanged},
	} {
		t.Run(test.name, func(t *testing.T) {
			assertGatewayStartupFence(t, test.initial, test.initialCerts, test.desired, test.desiredCerts)
		})
	}
}

func assertGatewayStartupFence(t *testing.T, initial gateway.DesiredState, initialCertificates []gateway.Certificate, desired gateway.DesiredState, desiredCertificates []gateway.Certificate) {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := store.RecordGatewayState(ctx, initial, initialCertificates); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	driver := &fakeGatewayDriver{blockRevision: initial.Revision, applyStarted: started, releaseApply: make(chan struct{})}
	restored := make(chan error, 1)
	go func() { restored <- (Client{GatewayDriver: driver}).PrepareGatewayStartup(ctx, store) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("startup restore did not reach the gated driver")
	}
	applied := make(chan error, 1)
	go func() { applied <- applyGatewayDesiredState(ctx, store, driver, desired, desiredCertificates) }()
	select {
	case err := <-applied:
		t.Fatalf("revision %d bypassed the startup restore fence: %v", desired.Revision, err)
	case <-time.After(50 * time.Millisecond):
	}
	close(driver.releaseApply)
	if err := <-restored; err != nil {
		t.Fatal(err)
	}
	if err := <-applied; err != nil {
		t.Fatal(err)
	}
	states := driver.appliedStates()
	if len(states) != 2 || states[0].Revision != initial.Revision || states[1].Revision != desired.Revision {
		t.Fatalf("live gateway ordering = %#v, want revisions %d then %d", states, initial.Revision, desired.Revision)
	}
	persisted, err := store.GatewayState(ctx)
	if err != nil || persisted.Desired.Revision != desired.Revision {
		t.Fatalf("persisted gateway = %#v, err=%v", persisted, err)
	}
	live, liveCertificates := driver.CurrentConfiguration()
	wantHash, err := gateway.ConfigurationHash(desired, desiredCertificates)
	if err != nil {
		t.Fatal(err)
	}
	liveHash, err := gateway.ConfigurationHash(live, liveCertificates)
	if err != nil || liveHash != wantHash || persisted.ConfigHash != wantHash {
		t.Fatalf("gateway hashes live=%q persisted=%q want=%q err=%v", liveHash, persisted.ConfigHash, wantHash, err)
	}
}

func TestGatewayRejectsConflictingReplayAtSameRevision(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	driver := &fakeGatewayDriver{}
	if err := applyGatewayDesiredState(context.Background(), store, driver, gatewayState(4, 3000), nil); err != nil {
		t.Fatal(err)
	}
	if err := applyGatewayDesiredState(context.Background(), store, driver, gatewayState(4, 3100), nil); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting revision replay was accepted: %v", err)
	}
	if states := driver.appliedStates(); len(states) != 1 || states[0].Routes[0].Upstreams[0].Port != 3000 {
		t.Fatalf("conflicting replay changed live state: %#v", states)
	}
}

func TestCaddyConfigurationUsesOnlyGatewayListenersAndMeshUpstreams(t *testing.T) {
	payload, err := caddyConfiguration(gatewayState(1, 3000), nil, "unix//run/vastora/caddy-admin.sock")
	if err != nil {
		t.Fatal(err)
	}
	var configuration map[string]any
	if err := json.Unmarshal(payload, &configuration); err != nil {
		t.Fatal(err)
	}
	admin := configuration["admin"].(map[string]any)
	if admin["listen"] != "unix//run/vastora/caddy-admin.sock" {
		t.Fatalf("Caddy Admin API is not using its Unix socket: %#v", admin)
	}
	encoded := string(payload)
	for _, wanted := range []string{`"listen":["0.0.0.0:10080"]`, `"automatic_https":{"disable":true}`, `"dial":"100.64.0.10:3000"`, `"host":["cpa.apps.example.test"]`} {
		if !strings.Contains(encoded, wanted) {
			t.Fatalf("Caddy configuration missing %s: %s", wanted, encoded)
		}
	}
}

func TestCaddyHTTPSRouteEnablesTLSAndRedirectsPlaintext(t *testing.T) {
	state := gatewayState(1, 3000)
	state.Routes[0].TLSEnabled = true
	certificate := testGatewayCertificate(t, state.Routes[0].Hostname)
	payload, err := caddyConfiguration(state, []gateway.Certificate{certificate}, "unix//run/vastora/caddy-admin.sock")
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	for _, wanted := range []string{`"listen":["0.0.0.0:10080"]`, `"listen":["0.0.0.0:10443"]`, `"tls_connection_policies":[{}]`, `"load_pem"`, `"tags":["vastora-cpa.apps.example.test"]`, `"status_code":308`, `"Location":["https://{http.request.host}{http.request.uri}"]`, `"host":["cpa.apps.example.test"]`} {
		if !strings.Contains(encoded, wanted) {
			t.Fatalf("HTTPS Caddy configuration missing %s: %s", wanted, encoded)
		}
	}
	if strings.Contains(encoded, "automation") {
		t.Fatalf("private HTTPS unexpectedly delegated certificate issuance to Caddy: %s", encoded)
	}
	if strings.Count(encoded, `"handler":"reverse_proxy"`) != 1 {
		t.Fatalf("HTTP redirect listener unexpectedly proxies application traffic: %s", encoded)
	}
}

func TestCaddyHTTPSRouteAcceptsSiteWildcardCertificate(t *testing.T) {
	state := gatewayState(1, 3000)
	state.Routes[0].Hostname = "panel-3x-ui.home.example.test"
	state.Routes[0].TLSEnabled = true
	certificate := testGatewayCertificate(t, "*.home.example.test")
	payload, err := caddyConfiguration(state, []gateway.Certificate{certificate}, "unix//run/vastora/caddy-admin.sock")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"host":["panel-3x-ui.home.example.test"]`) {
		t.Fatalf("wildcard certificate route missing from Caddy configuration: %s", payload)
	}
}

func TestGatewayRejectsMismatchedOrWrongHostnameCertificates(t *testing.T) {
	first := testGatewayCertificate(t, "first.example.test")
	second := testGatewayCertificate(t, "second.example.test")
	mismatched := first
	mismatched.PrivateKeyPEM = second.PrivateKeyPEM
	if err := gateway.ValidateCertificates([]gateway.Certificate{mismatched}); err == nil {
		t.Fatal("mismatched certificate and private key were accepted")
	}
	wrongHostname := first
	wrongHostname.Hostname = "other.example.test"
	if err := gateway.ValidateCertificates([]gateway.Certificate{wrongHostname}); err == nil {
		t.Fatal("certificate for another hostname was accepted")
	}
}

func TestShared443KeepsCaddyOnItsPrivateContainerSocket(t *testing.T) {
	state := gateway.DesiredState{
		Revision:  1,
		Listeners: []gateway.Listener{{Kind: "public", Address: "203.0.113.10", HTTPPort: 80, HTTPSPort: 443}},
		Routes:    []gateway.Route{{ID: "center", Hostname: "center.example.test", Protocol: "http", TLSEnabled: true, ListenerKind: "public", Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: 8080}}}},
		SharedHTTPS: &gateway.SharedHTTPS{Address: "203.0.113.10", Port: 443, CaddyAddress: "vastora-gateway-caddy", CaddyPort: 443, Routes: []gateway.Layer4Route{
			{ID: "vless", Hostname: "vless.example.test", ProxyProtocol: gateway.ProxyProtocolV2, Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: 2443}}},
			{ID: "raw", Hostname: "raw.example.test", Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: 3443}}},
		}},
	}
	payload, err := caddyConfiguration(state, nil, "unix//run/vastora/caddy-admin.sock")
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	if !strings.Contains(encoded, `"listen":["0.0.0.0:443"]`) || strings.Contains(encoded, `"listen":["203.0.113.10:443"]`) {
		t.Fatalf("shared 443 did not keep Caddy on its private container socket: %s", encoded)
	}
	configuration, err := haproxyConfiguration(*state.SharedHTTPS)
	if err != nil {
		t.Fatal(err)
	}
	haproxy := string(configuration)
	for _, wanted := range []string{"bind 0.0.0.0:443", "req.ssl_sni -i vless.example.test", "server caddy vastora-gateway-caddy:443 check", "server upstream-0 127.0.0.1:2443 check send-proxy-v2", "server upstream-0 127.0.0.1:3443 check"} {
		if !strings.Contains(haproxy, wanted) {
			t.Fatalf("HAProxy configuration missing %q: %s", wanted, haproxy)
		}
	}
	if strings.Contains(haproxy, "127.0.0.1:3443 check send-proxy") || strings.Contains(haproxy, "server caddy vastora-gateway-caddy:443 check send-proxy") {
		t.Fatalf("Proxy Protocol leaked to an unrelated backend: %s", haproxy)
	}
	invalid := *state.SharedHTTPS
	invalid.Routes = append([]gateway.Layer4Route(nil), invalid.Routes...)
	invalid.Routes[0].ProxyProtocol = "v1"
	if _, err := haproxyConfiguration(invalid); err == nil {
		t.Fatal("unsupported route-scoped Proxy Protocol mode was accepted")
	}
}

func TestFailedShared443RestoresCaddyToPublic443(t *testing.T) {
	var loaded [][]byte
	admin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/load" {
			http.NotFound(writer, request)
			return
		}
		defer request.Body.Close()
		payload, _ := io.ReadAll(request.Body)
		loaded = append(loaded, payload)
		writer.WriteHeader(http.StatusOK)
	}))
	defer admin.Close()
	caddy, err := NewCaddyGatewayDriver(admin.URL)
	if err != nil {
		t.Fatal(err)
	}
	layer4 := &fakeLayer4Provisioner{applyErr: errors.New("HAProxy failed")}
	runtime := &fakeGatewayRuntimeProvisioner{}
	driver := &ManagedGatewayDriver{Caddy: caddy, Layer4: layer4, Runtime: runtime}
	state := gateway.DesiredState{
		Revision:  1,
		Listeners: []gateway.Listener{{Kind: "public", Address: "203.0.113.10", HTTPPort: 80, HTTPSPort: 443}},
		Routes: []gateway.Route{{
			ID: "system-center", Hostname: "center.example.test", Protocol: "http", TLSEnabled: true, ListenerKind: "public", System: true,
			Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: 8080}},
		}},
		SharedHTTPS: &gateway.SharedHTTPS{Address: "203.0.113.10", Port: 443, CaddyAddress: "vastora-gateway-caddy", CaddyPort: 443, Routes: []gateway.Layer4Route{{ID: "vless", Hostname: "vless.example.test", Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: 2443}}}}},
	}
	if err := driver.ApplyConfiguration(context.Background(), state, nil); err == nil {
		t.Fatal("failed HAProxy transition was accepted")
	}
	if len(loaded) != 2 {
		t.Fatalf("Caddy was not restored after HAProxy failure: %d loads", len(loaded))
	}
	last := string(loaded[len(loaded)-1])
	if !strings.Contains(last, `"listen":["0.0.0.0:443"]`) || len(runtime.states) != 2 || runtime.states[1].SharedHTTPS != nil {
		t.Fatalf("Caddy runtime did not return ownership of public 443: %s states=%#v", last, runtime.states)
	}
}

func TestHAProxyContainerBootstrapsConfigurationInWritableTmpfs(t *testing.T) {
	configuration := []byte("global\n  maxconn 4096\n")
	options := haproxyContainerCreateOptions(
		DockerLayer4Provisioner{Image: DefaultHAProxyImage, Container: defaultHAProxyContainer},
		gateway.SharedHTTPS{Address: "203.0.113.10", Port: 443},
		configuration,
	)
	if !options.HostConfig.ReadonlyRootfs {
		t.Fatal("HAProxy container root filesystem must remain read-only")
	}
	if tmpfs := options.HostConfig.Tmpfs[haproxyConfigurationDir]; !strings.Contains(tmpfs, "rw") {
		t.Fatalf("HAProxy configuration directory is not writable tmpfs: %q", tmpfs)
	}
	if got, want := strings.Join(options.Config.Entrypoint, " "), "/bin/sh -ec"; got != want {
		t.Fatalf("unexpected HAProxy bootstrap entrypoint: got %q want %q", got, want)
	}
	if got, want := options.Config.Env, []string{haproxyConfigurationEnv + "=" + string(configuration)}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("HAProxy configuration was not passed to its bootstrap process: %#v", got)
	}
	if len(options.Config.Cmd) != 1 || !strings.Contains(options.Config.Cmd[0], "exec haproxy") || !strings.Contains(options.Config.Cmd[0], haproxyConfigurationPath) {
		t.Fatalf("HAProxy bootstrap command does not install and launch the configuration: %#v", options.Config.Cmd)
	}
}

func TestCaddyConfigurationKeepsLANAndHeadscaleListenersSeparate(t *testing.T) {
	state := gatewayState(1, 8317)
	state.Listeners = append(state.Listeners, gateway.Listener{Kind: "lan", Address: "192.168.1.2", HTTPPort: 80, HTTPSPort: 443})
	state.Routes = append(state.Routes, gateway.Route{ID: "keeper-route", Hostname: "keeper.lan.example.test", Protocol: "http", ListenerKind: "lan", Upstreams: []gateway.Upstream{{Address: "192.168.1.10", Port: 8080}}})
	payload, err := caddyConfiguration(state, nil, "unix//run/vastora/caddy-admin.sock")
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	for _, wanted := range []string{`"listen":["0.0.0.0:10080"]`, `"listen":["0.0.0.0:11080"]`, `"host":["cpa.apps.example.test"]`, `"host":["keeper.lan.example.test"]`} {
		if !strings.Contains(encoded, wanted) {
			t.Fatalf("multi-listener Caddy configuration missing %s: %s", wanted, encoded)
		}
	}
}

func TestCaddyHTTPSServiceUsesTLSUpstreamTransport(t *testing.T) {
	state := gatewayState(1, 8443)
	state.Routes[0].Protocol = "https"
	payload, err := caddyConfiguration(state, nil, "unix//run/vastora/caddy-admin.sock")
	if err != nil {
		t.Fatal(err)
	}
	if encoded := string(payload); !strings.Contains(encoded, `"transport":{"protocol":"http","tls":{}}`) {
		t.Fatalf("HTTPS Service did not enable verified upstream TLS: %s", encoded)
	}
}

func TestCaddyDriverAcceptsOnlyPrivateAdminEndpoints(t *testing.T) {
	driver, err := NewCaddyGatewayDriver("unix:///run/vastora/caddy-admin.sock")
	if err != nil {
		t.Fatal(err)
	}
	if driver.AdminListen != "unix//run/vastora/caddy-admin.sock" || driver.AdminSocketPath != "/run/vastora/caddy-admin.sock" || driver.AdminURL != "http://localhost" {
		t.Fatalf("unexpected Unix Admin API configuration: %#v", driver)
	}
	transport, ok := driver.HTTPClient.Transport.(*http.Transport)
	if !ok || !transport.DisableKeepAlives {
		t.Fatal("Unix Admin API must reconnect after the managed Caddy container is replaced")
	}
	for _, endpoint := range []string{"http://127.0.0.1:2019"} {
		if _, err := NewCaddyGatewayDriver(endpoint); err != nil {
			t.Fatalf("private endpoint %q was rejected: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{"http://0.0.0.0:2019", "http://[::1]:2019", "http://example.com:2019", "unix://relative.sock"} {
		if _, err := NewCaddyGatewayDriver(endpoint); err == nil {
			t.Fatalf("unsafe endpoint %q was accepted", endpoint)
		}
	}
}

func TestGatewayProvisionerSharesUnixAdminSocketWithHost(t *testing.T) {
	settings, err := (DockerGatewayProvisioner{}).settings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.AdminListen != "unix//run/vastora/caddy-admin.sock" || settings.AdminSocketPath != "/run/vastora/caddy-admin.sock" {
		t.Fatalf("unexpected default Admin API settings: %#v", settings)
	}
	mounts := gatewayMounts(settings)
	if len(mounts) != 3 || mounts[2].Type != mount.TypeBind || mounts[2].Source != "/run/vastora" || mounts[2].Target != "/run/vastora" {
		t.Fatalf("Admin socket directory is not shared with the host: %#v", mounts)
	}
	loopback, err := (DockerGatewayProvisioner{AdminListen: "127.0.0.1:2019"}).settings()
	if err != nil {
		t.Fatal(err)
	}
	if loopback.AdminSocketPath != "" {
		t.Fatalf("loopback Admin API unexpectedly configured a socket: %#v", loopback)
	}
	if _, err := (DockerGatewayProvisioner{AdminSocketPath: "relative.sock"}).settings(); err == nil {
		t.Fatal("relative Admin socket path was accepted")
	}
}

func TestCaddyPortsMatchAllowsImageExposedPorts(t *testing.T) {
	https := dockernetwork.MustParsePort("443/tcp")
	admin := dockernetwork.MustParsePort("2019/tcp")
	expectedExposed := dockernetwork.PortSet{https: {}}
	expectedBindings := dockernetwork.PortMap{https: []dockernetwork.PortBinding{{HostIP: netip.MustParseAddr("100.64.0.1"), HostPort: "443"}}}
	config := &container.Config{ExposedPorts: dockernetwork.PortSet{https: {}, admin: {}}}
	host := &container.HostConfig{PortBindings: expectedBindings}
	if !caddyPortsMatch(config, host, expectedExposed, expectedBindings) {
		t.Fatal("image-declared Admin port forced an unnecessary Caddy replacement")
	}
	host.PortBindings = dockernetwork.PortMap{https: []dockernetwork.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "443"}}}
	if caddyPortsMatch(config, host, expectedExposed, expectedBindings) {
		t.Fatal("changed host port binding was treated as current")
	}
}

func TestWaitForCaddyAdminSocket(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "vastora-caddy-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "admin.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := waitForCaddyAdminSocket(context.Background(), path); err != nil {
		t.Fatal(err)
	}
}

func TestSystemGatewayProtectionIsRecoveredFromCompleteDesiredState(t *testing.T) {
	desired := gateway.DesiredState{
		Revision: 1,
		Listeners: []gateway.Listener{
			{Kind: "public", Address: "192.0.2.10", HTTPPort: 80, HTTPSPort: 443},
			{Kind: "headscale", Address: "100.64.0.1", HTTPPort: 80, HTTPSPort: 443},
			{Kind: "system", Address: "127.0.0.1", HTTPPort: 80, HTTPSPort: 443},
		},
		Routes: []gateway.Route{
			{ID: "system-center", Hostname: "center.example.test", Protocol: "http", Upstreams: []gateway.Upstream{{Address: "vastora-center", Port: 8080}}, TLSEnabled: true, ListenerKind: "headscale", System: true},
			{ID: "system-center-local", Hostname: "center.example.test", Protocol: "http", Upstreams: []gateway.Upstream{{Address: "vastora-center", Port: 8080}}, TLSEnabled: true, ListenerKind: "system", System: true},
			{ID: "system-headscale", Hostname: "headscale.example.test", Protocol: "http", Upstreams: []gateway.Upstream{{Address: "vastora-center-headscale", Port: 8081}}, TLSEnabled: true, ListenerKind: "public", System: true},
			{ID: "system-headscale-local", Hostname: "headscale.example.test", Protocol: "http", Upstreams: []gateway.Upstream{{Address: "vastora-center-headscale", Port: 8081}}, TLSEnabled: true, ListenerKind: "system", System: true},
			{ID: "system-agent-bootstrap", Hostname: "headscale.example.test", Path: "/install/agent.sh", Protocol: "http", Upstreams: []gateway.Upstream{{Address: "vastora-center", Port: 8080}}, TLSEnabled: true, ListenerKind: "public", System: true},
			{ID: "system-agent-binary-bootstrap", Hostname: "headscale.example.test", Path: "/api/v1/agent-binaries/*", Protocol: "http", Upstreams: []gateway.Upstream{{Address: "vastora-center", Port: 8080}}, TLSEnabled: true, ListenerKind: "public", System: true},
			{ID: "system-agent-decommission-callback", Hostname: "headscale.example.test", Path: "/api/v1/agent-decommission-results/*", Protocol: "http", Upstreams: []gateway.Upstream{{Address: "vastora-center", Port: 8080}}, TLSEnabled: true, ListenerKind: "public", System: true},
		},
	}
	label, err := systemServicesForGatewayTransition("", &desired)
	if err != nil {
		t.Fatal(err)
	}
	if label != gatewayruntime.SystemServices {
		t.Fatalf("recovered system services label = %q, want %q", label, gatewayruntime.SystemServices)
	}
	desired.Routes = desired.Routes[1:]
	if _, err := systemServicesForGatewayTransition(gatewayruntime.SystemServices, &desired); err == nil {
		t.Fatal("incomplete desired state was allowed to replace a protected system gateway")
	}
}

func TestProtectedSystemGatewayRejectsStateWithoutSystemRoutes(t *testing.T) {
	driver, err := NewCaddyGatewayDriver("http://127.0.0.1:2019")
	if err != nil {
		t.Fatal(err)
	}
	driver.SystemGateway = staticSystemGatewayInspector{"center", "headscale"}
	err = driver.ApplyConfiguration(context.Background(), gatewayState(1, 3000), nil)
	if err == nil || !strings.Contains(err.Error(), `without headscale route "system-center"`) {
		t.Fatalf("unsafe stale gateway state was not rejected: %v", err)
	}
}

func TestProtectedSystemGatewayAcceptsCompleteSystemRoutes(t *testing.T) {
	desired := gateway.DesiredState{
		Revision: 1,
		Listeners: []gateway.Listener{
			{Kind: "public", Address: "192.0.2.10", HTTPPort: 80, HTTPSPort: 443},
			{Kind: "headscale", Address: "100.64.0.1", HTTPPort: 80, HTTPSPort: 443},
			{Kind: "system", Address: "127.0.0.1", HTTPPort: 80, HTTPSPort: 443},
		},
	}
	for _, service := range []struct {
		name string
		port int
	}{{"center", 8080}, {"headscale", 8081}} {
		listener := "public"
		if service.name == "center" {
			listener = "headscale"
		}
		desired.Routes = append(desired.Routes,
			gateway.Route{ID: "system-" + service.name, Hostname: service.name + ".example.test", Protocol: "http", Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: service.port}}, TLSEnabled: true, ListenerKind: listener, System: true},
			gateway.Route{ID: "system-" + service.name + "-local", Hostname: service.name + ".example.test", Protocol: "http", Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: service.port}}, TLSEnabled: true, ListenerKind: "system", System: true},
		)
	}
	desired.Routes = append(desired.Routes, gateway.Route{ID: "system-agent-bootstrap", Hostname: "headscale.example.test", Path: "/install/agent.sh", Protocol: "http", Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: 8080}}, TLSEnabled: true, ListenerKind: "public", System: true})
	desired.Routes = append(desired.Routes, gateway.Route{ID: "system-agent-binary-bootstrap", Hostname: "headscale.example.test", Path: "/api/v1/agent-binaries/*", Protocol: "http", Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: 8080}}, TLSEnabled: true, ListenerKind: "public", System: true})
	desired.Routes = append(desired.Routes, gateway.Route{ID: "system-agent-decommission-callback", Hostname: "headscale.example.test", Path: "/api/v1/agent-decommission-results/*", Protocol: "http", Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: 8080}}, TLSEnabled: true, ListenerKind: "public", System: true})
	if err := validateProtectedSystemRoutes(desired, []string{"center", "headscale"}); err != nil {
		t.Fatalf("complete protected system state was rejected: %v", err)
	}
}
