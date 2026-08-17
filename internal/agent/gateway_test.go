package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/gateway"
)

type fakeGatewayDriver struct {
	applied    []gateway.DesiredState
	fail       bool
	healthFail bool
}

func (driver *fakeGatewayDriver) ApplyConfiguration(_ context.Context, desired gateway.DesiredState) error {
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

func TestGatewayHealthFailureDoesNotAdvanceLastKnownGood(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	driver := &fakeGatewayDriver{}
	if err := applyGatewayDesiredState(ctx, store, driver, gatewayState(1, 3000)); err != nil {
		t.Fatal(err)
	}
	driver.healthFail = true
	if err := applyGatewayDesiredState(ctx, store, driver, gatewayState(2, 3100)); err == nil {
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
	if err := applyGatewayDesiredState(ctx, store, driver, gatewayState(1, 3000)); err != nil {
		t.Fatal(err)
	}
	if err := applyGatewayDesiredState(ctx, store, driver, gatewayState(1, 3000)); err != nil {
		t.Fatal(err)
	}
	if len(driver.applied) != 1 {
		t.Fatalf("duplicate revision was applied %d times", len(driver.applied))
	}
	driver.fail = true
	if err := applyGatewayDesiredState(ctx, store, driver, gatewayState(2, 3100)); err == nil {
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
	if err := applyGatewayDesiredState(ctx, store, initial, gatewayState(7, 3000)); err != nil {
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
	payload, err := caddyConfiguration(gatewayState(1, 3000), "unix//run/vastora/caddy-admin.sock")
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
	payload, err := caddyConfiguration(state, "unix//run/vastora/caddy-admin.sock")
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(payload)
	for _, wanted := range []string{`"listen":["100.64.0.2:80"]`, `"listen":["100.64.0.2:443"]`, `"tls_connection_policies":[{}]`, `"status_code":308`, `"Location":["https://{http.request.host}{http.request.uri}"]`, `"host":["cpa.apps.example.test"]`} {
		if !strings.Contains(encoded, wanted) {
			t.Fatalf("HTTPS Caddy configuration missing %s: %s", wanted, encoded)
		}
	}
	if strings.Count(encoded, `"handler":"reverse_proxy"`) != 1 {
		t.Fatalf("HTTP redirect listener unexpectedly proxies application traffic: %s", encoded)
	}
}

func TestCaddyConfigurationKeepsLANAndHeadscaleListenersSeparate(t *testing.T) {
	state := gatewayState(1, 8317)
	state.Listeners = append(state.Listeners, gateway.Listener{Kind: "lan", Address: "192.168.1.2", HTTPPort: 80, HTTPSPort: 443})
	state.Routes = append(state.Routes, gateway.Route{ID: "keeper-route", Hostname: "keeper.lan.example.test", Protocol: "http", ListenerKind: "lan", Upstreams: []gateway.Upstream{{Address: "192.168.1.10", Port: 8080}}})
	payload, err := caddyConfiguration(state, "unix//run/vastora/caddy-admin.sock")
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
	payload, err := caddyConfiguration(state, "unix//run/vastora/caddy-admin.sock")
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
	if driver.AdminListen != "unix//run/vastora/caddy-admin.sock" || driver.AdminURL != "http://localhost" {
		t.Fatalf("unexpected Unix Admin API configuration: %#v", driver)
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
