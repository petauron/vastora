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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/mount"
	"github.com/petauron/vastora/internal/gateway"
)

type fakeGatewayDriver struct {
	applied    []gateway.DesiredState
	fail       bool
	healthFail bool
}

type fakeLayer4Provisioner struct {
	applyErr error
	removed  int
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

func (driver *fakeGatewayDriver) ApplyConfiguration(_ context.Context, desired gateway.DesiredState, _ []gateway.Certificate) error {
	if driver.fail {
		return errors.New("Caddy rejected configuration")
	}
	driver.applied = append(driver.applied, desired.Sorted())
	return nil
}
func (driver *fakeGatewayDriver) ApplyRoute(context.Context, gateway.Route) error { return nil }
func (driver *fakeGatewayDriver) DeleteRoute(context.Context, string) error       { return nil }
func (driver *fakeGatewayDriver) ListRoutes(context.Context) ([]gateway.Route, error) {
	if len(driver.applied) == 0 {
		return nil, nil
	}
	return driver.applied[len(driver.applied)-1].Routes, nil
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
	for _, wanted := range []string{`"listen":["100.64.0.2:80"]`, `"dial":"100.64.0.10:3000"`, `"host":["cpa.apps.example.test"]`} {
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
	for _, wanted := range []string{`"listen":["100.64.0.2:80"]`, `"listen":["100.64.0.2:443"]`, `"tls_connection_policies":[{}]`, `"load_pem"`, `"tags":["vastora-cpa.apps.example.test"]`, `"status_code":308`, `"Location":["https://{http.request.host}{http.request.uri}"]`, `"host":["cpa.apps.example.test"]`} {
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

func TestShared443MovesCaddyHTTPSToLoopback(t *testing.T) {
	state := gateway.DesiredState{
		Revision:    1,
		Listeners:   []gateway.Listener{{Kind: "public", Address: "203.0.113.10", HTTPPort: 80, HTTPSPort: 443}},
		Routes:      []gateway.Route{{ID: "center", Hostname: "center.example.test", Protocol: "http", TLSEnabled: true, ListenerKind: "public", Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: 8080}}}},
		SharedHTTPS: &gateway.SharedHTTPS{Address: "203.0.113.10", Port: 443, CaddyAddress: "127.0.0.1", CaddyPort: 8443, Routes: []gateway.Layer4Route{{ID: "vless", Hostname: "vless.example.test", Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: 2443}}}}},
	}
	payload, err := caddyConfiguration(state, nil, "unix//run/vastora/caddy-admin.sock")
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	if !strings.Contains(encoded, `"listen":["127.0.0.1:8443"]`) || strings.Contains(encoded, `"listen":["203.0.113.10:443"]`) {
		t.Fatalf("shared 443 did not move Caddy HTTPS to loopback: %s", encoded)
	}
	configuration, err := haproxyConfiguration(*state.SharedHTTPS)
	if err != nil {
		t.Fatal(err)
	}
	haproxy := string(configuration)
	for _, wanted := range []string{"bind 203.0.113.10:443", "req.ssl_sni -i vless.example.test", "server caddy 127.0.0.1:8443 check", "server upstream-0 127.0.0.1:2443 check"} {
		if !strings.Contains(haproxy, wanted) {
			t.Fatalf("HAProxy configuration missing %q: %s", wanted, haproxy)
		}
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
	driver := &ManagedGatewayDriver{Caddy: caddy, Layer4: layer4}
	state := gateway.DesiredState{
		Revision:  1,
		Listeners: []gateway.Listener{{Kind: "public", Address: "203.0.113.10", HTTPPort: 80, HTTPSPort: 443}},
		Routes: []gateway.Route{{
			ID: "system-center", Hostname: "center.example.test", Protocol: "http", TLSEnabled: true, ListenerKind: "public", System: true,
			Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: 8080}},
		}},
		SharedHTTPS: &gateway.SharedHTTPS{Address: "203.0.113.10", Port: 443, CaddyAddress: "127.0.0.1", CaddyPort: 8443, Routes: []gateway.Layer4Route{{ID: "vless", Hostname: "vless.example.test", Upstreams: []gateway.Upstream{{Address: "127.0.0.1", Port: 2443}}}}},
	}
	if err := driver.ApplyConfiguration(context.Background(), state, nil); err == nil {
		t.Fatal("failed HAProxy transition was accepted")
	}
	if len(loaded) != 2 {
		t.Fatalf("Caddy was not restored after HAProxy failure: %d loads", len(loaded))
	}
	last := string(loaded[len(loaded)-1])
	if !strings.Contains(last, `"listen":["203.0.113.10:443"]`) || strings.Contains(last, `"listen":["127.0.0.1:8443"]`) {
		t.Fatalf("Caddy did not return to public 443: %s", last)
	}
}

func TestHAProxyContainerBootstrapsConfigurationInWritableTmpfs(t *testing.T) {
	configuration := []byte("global\n  maxconn 4096\n")
	options := haproxyContainerCreateOptions(
		DockerLayer4Provisioner{Image: defaultHAProxyImage, Container: defaultHAProxyContainer},
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
	for _, wanted := range []string{`"listen":["100.64.0.2:80"]`, `"listen":["192.168.1.2:80"]`, `"host":["cpa.apps.example.test"]`, `"host":["keeper.lan.example.test"]`} {
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
	for _, endpoint := range []string{"http://127.0.0.1:2019", "http://[::1]:2019"} {
		if _, err := NewCaddyGatewayDriver(endpoint); err != nil {
			t.Fatalf("private endpoint %q was rejected: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{"http://0.0.0.0:2019", "http://example.com:2019", "unix://relative.sock"} {
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
	if err := validateProtectedSystemRoutes(desired, []string{"center", "headscale"}); err != nil {
		t.Fatalf("complete protected system state was rejected: %v", err)
	}
}
