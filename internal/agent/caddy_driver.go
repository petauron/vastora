package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/gatewayruntime"
)

type CaddyGatewayDriver struct {
	AdminURL        string
	AdminListen     string
	AdminSocketPath string
	HTTPClient      *http.Client
	SystemGateway   SystemGatewayInspector

	mutationMu   sync.Mutex
	mu           sync.RWMutex
	state        gateway.DesiredState
	certificates []gateway.Certificate
}

func NewCaddyGatewayDriver(adminURL string) (*CaddyGatewayDriver, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(adminURL), "/"))
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("agent: Caddy Admin API must use a Unix socket or loopback HTTP")
	}
	if parsed.Scheme == "unix" {
		if parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") {
			return nil, errors.New("agent: Caddy Admin API Unix socket path must be absolute")
		}
		socketPath := parsed.Path
		transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		}}
		return &CaddyGatewayDriver{
			AdminURL:        "http://localhost",
			AdminListen:     "unix/" + socketPath,
			AdminSocketPath: socketPath,
			HTTPClient:      &http.Client{Transport: transport, Timeout: 15 * time.Second},
		}, nil
	}
	if parsed.Scheme != "http" || parsed.Path != "" || !isLoopbackHost(parsed.Hostname()) {
		return nil, errors.New("agent: Caddy Admin API must use a Unix socket or loopback HTTP")
	}
	if parsed.Port() == "" {
		return nil, errors.New("agent: Caddy Admin API port is required")
	}
	return &CaddyGatewayDriver{AdminURL: parsed.String(), AdminListen: parsed.Host, HTTPClient: &http.Client{Timeout: 15 * time.Second}}, nil
}

func (driver *CaddyGatewayDriver) ApplyConfiguration(ctx context.Context, desired gateway.DesiredState, certificates []gateway.Certificate) error {
	driver.mutationMu.Lock()
	defer driver.mutationMu.Unlock()
	return driver.applyConfiguration(ctx, desired, certificates)
}

func (driver *CaddyGatewayDriver) applyConfiguration(ctx context.Context, desired gateway.DesiredState, certificates []gateway.Certificate) error {
	if err := desired.Validate(); err != nil {
		return err
	}
	if err := gateway.ValidateCertificatesForState(desired, certificates); err != nil {
		return err
	}
	if driver.SystemGateway != nil {
		services, err := driver.SystemGateway.ProtectedSystemServices(ctx)
		if err != nil {
			return fmt.Errorf("agent: inspect protected system gateway: %w", err)
		}
		if err := validateProtectedSystemRoutes(desired, services); err != nil {
			return err
		}
	}
	configuration, err := caddyConfiguration(desired.Sorted(), certificates, driver.AdminListen)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, driver.AdminURL+"/load", bytes.NewReader(configuration))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := driver.client().Do(request)
	if err != nil {
		return fmt.Errorf("request Caddy Admin API: %w", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = response.Status
		}
		return fmt.Errorf("caddy rejected configuration: %s", message)
	}
	driver.mu.Lock()
	driver.state = desired.Sorted()
	driver.certificates = append([]gateway.Certificate(nil), certificates...)
	driver.mu.Unlock()
	return nil
}

func validateProtectedSystemRoutes(desired gateway.DesiredState, services []string) error {
	if len(services) == 0 {
		return nil
	}
	routes := make(map[string]gateway.Route, len(desired.Routes))
	for _, route := range desired.Routes {
		routes[route.ID] = route
	}
	protected := make(map[string]bool, len(services))
	for _, service := range services {
		protected[service] = true
		listener := "public"
		if service == "center" {
			listener = "headscale"
		}
		required := []struct{ id, listener, path string }{
			{"system-" + service, listener, ""},
			{"system-" + service + "-local", "system", ""},
		}
		for _, expected := range required {
			route, exists := routes[expected.id]
			if !exists || !route.System || !route.TLSEnabled || route.ListenerKind != expected.listener || route.Path != expected.path {
				return fmt.Errorf("agent: refusing to replace protected system gateway without %s route %q", expected.listener, expected.id)
			}
		}
	}
	if protected["center"] && protected["headscale"] {
		required := []struct{ id, path string }{
			{"system-agent-bootstrap", "/install/agent.sh"},
			{"system-agent-binary-bootstrap", "/api/v1/agent-binaries/*"},
		}
		for _, expected := range required {
			route, exists := routes[expected.id]
			if !exists || !route.System || !route.TLSEnabled || route.ListenerKind != "public" || route.Path != expected.path {
				return errors.New("agent: refusing to replace protected system gateway without public Agent bootstrap routes")
			}
		}
	}
	return nil
}

func (driver *CaddyGatewayDriver) ApplyRoute(ctx context.Context, route gateway.Route) error {
	driver.mutationMu.Lock()
	defer driver.mutationMu.Unlock()
	driver.mu.RLock()
	next := driver.state.Sorted()
	certificates := append([]gateway.Certificate(nil), driver.certificates...)
	driver.mu.RUnlock()
	found := false
	for index := range next.Routes {
		if next.Routes[index].ID == route.ID {
			next.Routes[index] = route
			found = true
		}
	}
	if !found {
		next.Routes = append(next.Routes, route)
	}
	if next.Revision < 1 {
		next.Revision = 1
	} else {
		next.Revision++
	}
	return driver.applyConfiguration(ctx, next, certificates)
}

func (driver *CaddyGatewayDriver) DeleteRoute(ctx context.Context, routeID string) error {
	driver.mutationMu.Lock()
	defer driver.mutationMu.Unlock()
	driver.mu.RLock()
	current := driver.state.Sorted()
	certificates := append([]gateway.Certificate(nil), driver.certificates...)
	driver.mu.RUnlock()
	next := gateway.DesiredState{Revision: current.Revision, Routes: make([]gateway.Route, 0, len(current.Routes))}
	for _, route := range current.Routes {
		if route.ID != routeID {
			next.Routes = append(next.Routes, route)
		}
	}
	if next.Revision < 1 {
		next.Revision = 1
	} else {
		next.Revision++
	}
	return driver.applyConfiguration(ctx, next, certificates)
}

func (driver *CaddyGatewayDriver) ListRoutes(context.Context) ([]gateway.Route, error) {
	driver.mu.RLock()
	defer driver.mu.RUnlock()
	return driver.state.Sorted().Routes, nil
}

func (driver *CaddyGatewayDriver) CurrentConfiguration() (gateway.DesiredState, []gateway.Certificate) {
	driver.mu.RLock()
	defer driver.mu.RUnlock()
	return driver.state.Sorted(), append([]gateway.Certificate(nil), driver.certificates...)
}

func (driver *CaddyGatewayDriver) GetRouteStatus(ctx context.Context, routeID string) (string, error) {
	routes, _ := driver.ListRoutes(ctx)
	for _, route := range routes {
		if route.ID == routeID {
			if err := driver.Health(ctx); err != nil {
				return "failed", err
			}
			return "ready", nil
		}
	}
	return "", errors.New("agent: route not found")
}

func (driver *CaddyGatewayDriver) Health(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, driver.AdminURL+"/config/", nil)
	if err != nil {
		return err
	}
	response, err := driver.client().Do(request)
	if err != nil {
		return fmt.Errorf("agent: Caddy health: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("agent: Caddy health: %s", response.Status)
	}
	return nil
}

func (driver *CaddyGatewayDriver) client() *http.Client {
	if driver.HTTPClient != nil {
		return driver.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func caddyConfiguration(desired gateway.DesiredState, certificates []gateway.Certificate, adminListen string) ([]byte, error) {
	if err := gateway.ValidateCertificatesForState(desired, certificates); err != nil {
		return nil, err
	}
	type caddyRoute struct {
		Match    []map[string][]string `json:"match"`
		Handle   []map[string]any      `json:"handle"`
		Terminal bool                  `json:"terminal"`
	}
	servers := map[string]any{}
	for _, listener := range desired.Listeners {
		internalHTTPPort, internalHTTPSPort, ok := gatewayruntime.CaddyListenerPorts(listener.Kind)
		if !ok {
			return nil, fmt.Errorf("agent: unsupported Caddy listener kind %q", listener.Kind)
		}
		httpRoutes := make([]caddyRoute, 0)
		httpsRoutes := make([]caddyRoute, 0)
		redirectHosts := map[string]bool{}
		for _, route := range desired.Routes {
			if route.ListenerKind != listener.Kind {
				continue
			}
			upstreams := make([]map[string]string, 0, len(route.Upstreams))
			for _, upstream := range route.Upstreams {
				upstreams = append(upstreams, map[string]string{"dial": net.JoinHostPort(upstream.Address, strconv.Itoa(upstream.Port))})
			}
			proxy := map[string]any{"handler": "reverse_proxy", "upstreams": upstreams}
			if route.Protocol == "https" {
				proxy["transport"] = map[string]any{"protocol": "http", "tls": map[string]any{}}
			}
			matcher := map[string][]string{"host": {route.Hostname}}
			if route.Path != "" {
				matcher["path"] = []string{route.Path}
			}
			handlers := []map[string]any{proxy}
			candidate := caddyRoute{Match: []map[string][]string{matcher}, Handle: handlers, Terminal: true}
			if route.TLSEnabled {
				httpsRoutes = append(httpsRoutes, candidate)
				if !redirectHosts[route.Hostname] {
					redirect := map[string]any{"handler": "static_response", "status_code": 308, "headers": map[string][]string{"Location": {"https://{http.request.host}{http.request.uri}"}}}
					httpRoutes = append(httpRoutes, caddyRoute{Match: []map[string][]string{{"host": {route.Hostname}}}, Handle: []map[string]any{redirect}, Terminal: true})
					redirectHosts[route.Hostname] = true
				}
			} else {
				httpRoutes = append(httpRoutes, candidate)
			}
		}
		if len(httpRoutes) != 0 {
			servers["vastora-"+listener.Kind+"-http"] = map[string]any{
				"listen":          []string{net.JoinHostPort("0.0.0.0", strconv.Itoa(internalHTTPPort))},
				"routes":          httpRoutes,
				"automatic_https": map[string]any{"disable": true},
			}
		}
		if len(httpsRoutes) != 0 {
			servers["vastora-"+listener.Kind+"-https"] = map[string]any{"listen": []string{net.JoinHostPort("0.0.0.0", strconv.Itoa(internalHTTPSPort))}, "routes": httpsRoutes, "tls_connection_policies": []map[string]any{{}}}
		}
	}
	apps := map[string]any{"http": map[string]any{"servers": servers}}
	if len(certificates) != 0 {
		pairs := make([]map[string]any, 0, len(certificates))
		for _, certificate := range certificates {
			pairs = append(pairs, map[string]any{"certificate": certificate.CertificatePEM, "key": certificate.PrivateKeyPEM, "tags": []string{"vastora-" + certificate.Hostname}})
		}
		apps["tls"] = map[string]any{"certificates": map[string]any{"load_pem": pairs}}
	}
	configuration := map[string]any{"admin": map[string]any{"listen": adminListen}, "apps": apps}
	return json.Marshal(configuration)
}
