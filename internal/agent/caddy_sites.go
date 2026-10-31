package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/nodesite"
)

var errSharedCaddyRuntimeAbsent = errors.New("agent: shared Caddy runtime is absent")

// CaddySite is an owned website contribution, not arbitrary Caddy JSON. It is
// persisted only inside the encrypted node-site journal. Website packages supply
// the document; Agent owns listeners, routing, headers and certificate loading.
type CaddySite struct {
	ApplicationID string              `json:"applicationId"`
	Hostname      string              `json:"hostname"`
	Address       string              `json:"address"`
	HTTPPort      int                 `json:"httpPort"`
	TLSPort       int                 `json:"tlsPort,omitempty"`
	PublicListen  string              `json:"publicListen,omitempty"`
	HTML          string              `json:"html"`
	Certificate   gateway.Certificate `json:"certificate"`
	CaddyPath     string              `json:"caddyPath"`
	CaddySHA256   string              `json:"caddySha256"`
}

func (site CaddySite) validate() error {
	if site.ApplicationID == "" || len(site.ApplicationID) > 256 || strings.ContainsAny(site.ApplicationID, "\x00\r\n") || !nodesite.ValidHostname(site.Hostname) || !networking.IsPrivateServiceAddress(site.Address) || site.HTTPPort < 1024 || site.HTTPPort > 65535 || site.TLSPort < 0 || site.TLSPort > 65535 || (site.TLSPort != 0 && site.TLSPort < 1024) || site.HTTPPort == site.TLSPort || len(site.HTML) == 0 || len(site.HTML) > 512<<10 || !utf8.ValidString(site.HTML) {
		return errors.New("agent: invalid owned Caddy website")
	}
	if site.TLSPort != 0 {
		if site.Certificate.Hostname != site.Hostname {
			return errors.New("agent: website certificate identity mismatch")
		}
		// Stored expired certificates must not prevent other sites or system
		// routes from being reconfigured. New deliveries check validity dates in
		// nodeSiteCertificate before entering this durable configuration.
		if len(site.Certificate.CertificatePEM) > 65536 || len(site.Certificate.PrivateKeyPEM) > 16384 {
			return errors.New("agent: invalid stored website certificate")
		}
		pair, err := tls.X509KeyPair([]byte(site.Certificate.CertificatePEM), []byte(site.Certificate.PrivateKeyPEM))
		if err != nil || len(pair.Certificate) == 0 {
			return errors.New("agent: invalid stored website key pair")
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != site.Hostname || len(leaf.IPAddresses) != 0 || len(leaf.EmailAddresses) != 0 || len(leaf.URIs) != 0 {
			return errors.New("agent: stored website certificate identity mismatch")
		}
	}
	if site.PublicListen != "" {
		host, port, err := net.SplitHostPort(site.PublicListen)
		address := net.ParseIP(host)
		if err != nil || port != "443" || address == nil || address.IsUnspecified() || address.IsMulticast() || address.IsLinkLocalUnicast() {
			return errors.New("agent: website HTTPS requires an exact local address on port 443")
		}
	}
	return nil
}

func siteDocumentHandler(site CaddySite) map[string]any {
	return map[string]any{"handler": "static_response", "body": site.HTML, "headers": map[string][]string{
		"Content-Type":            {"text/html; charset=utf-8"},
		"Content-Security-Policy": {"default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"},
		"X-Content-Type-Options":  {"nosniff"}, "Referrer-Policy": {"no-referrer"},
	}}
}

func siteRoutes(site CaddySite, health bool) []any {
	routes := []any{map[string]any{"match": []any{map[string]any{"host": []string{site.Hostname}, "path": []string{"/"}, "method": []string{"GET", "HEAD"}}}, "handle": []any{siteDocumentHandler(site)}, "terminal": true}}
	if health {
		routes = append([]any{map[string]any{"match": []any{map[string]any{"path": []string{"/healthz"}, "method": []string{"GET", "HEAD"}}}, "handle": []any{map[string]any{"handler": "static_response", "status_code": 204}}, "terminal": true}}, routes...)
	}
	return routes
}

func caddyObject(parent map[string]any, key string) map[string]any {
	value, ok := parent[key].(map[string]any)
	if !ok {
		value = map[string]any{}
		parent[key] = value
	}
	return value
}

func caddyStringSlice(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		result := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}
	return nil
}

// composeCaddySites always starts with the complete gateway configuration. It
// never reads a live server configuration and never delegates /load to a page.
func composeCaddySites(raw []byte, desired gateway.DesiredState, sites []CaddySite, container bool) ([]byte, error) {
	var config map[string]any
	if json.Unmarshal(raw, &config) != nil {
		return nil, errors.New("agent: invalid shared Caddy base configuration")
	}
	servers := caddyObject(caddyObject(caddyObject(config, "apps"), "http"), "servers")
	sites = append([]CaddySite(nil), sites...)
	sort.Slice(sites, func(i, j int) bool { return sites[i].ApplicationID < sites[j].ApplicationID })
	seenID, seenHost := map[string]bool{}, map[string]bool{}
	for _, site := range sites {
		if err := site.validate(); err != nil {
			return nil, err
		}
		if seenID[site.ApplicationID] || seenHost[site.Hostname] {
			return nil, errors.New("agent: duplicate shared website identity")
		}
		seenID[site.ApplicationID], seenHost[site.Hostname] = true, true
		for _, route := range desired.Routes {
			if route.Hostname == site.Hostname && route.System {
				return nil, errors.New("agent: website cannot replace a system route")
			}
		}
		// A published website is served directly. Only the exact website's own
		// upstream can be replaced, so unrelated proxy routes stay untouched.
		for _, value := range servers {
			server, _ := value.(map[string]any)
			routes, _ := server["routes"].([]any)
			for _, value := range routes {
				route, _ := value.(map[string]any)
				matches, _ := route["match"].([]any)
				handlers, _ := route["handle"].([]any)
				if len(matches) != 1 || len(handlers) != 1 {
					continue
				}
				match, _ := matches[0].(map[string]any)
				hosts := caddyStringSlice(match["host"])
				if len(hosts) != 1 || hosts[0] != site.Hostname || match["header"] != nil {
					continue
				}
				paths := caddyStringSlice(match["path"])
				if len(paths) > 0 && (len(paths) != 1 || paths[0] != "/") {
					continue
				}
				proxy, _ := handlers[0].(map[string]any)
				upstreams, _ := proxy["upstreams"].([]any)
				if proxy["handler"] != "reverse_proxy" || len(upstreams) != 1 {
					continue
				}
				upstream, _ := upstreams[0].(map[string]any)
				if upstream["dial"] == net.JoinHostPort(site.Address, strconv.Itoa(site.HTTPPort)) {
					// Preserve methods/path semantics of the website document.
					match["path"], match["method"] = []string{"/"}, []string{"GET", "HEAD"}
					route["handle"] = []any{siteDocumentHandler(site)}
				}
			}
		}
		address := site.Address
		if container {
			address = "0.0.0.0"
		}
		http := map[string]any{"listen": []string{net.JoinHostPort(address, strconv.Itoa(site.HTTPPort))}, "routes": siteRoutes(site, true), "automatic_https": map[string]any{"disable": true}, "protocols": []string{"h1", "h2"}, "read_header_timeout": "10s", "idle_timeout": "30s", "max_header_bytes": 16384}
		if err := mergeSiteServer(servers, "site-"+packageIdentity(site.ApplicationID)+"-http", http); err != nil {
			return nil, err
		}
		if site.TLSPort == 0 {
			continue
		}
		address = "127.0.0.1"
		if container {
			address = "0.0.0.0"
		}
		https := map[string]any{"listen": []string{net.JoinHostPort(address, strconv.Itoa(site.TLSPort))}, "routes": siteRoutes(site, false), "automatic_https": map[string]any{"disable": true}, "protocols": []string{"h1", "h2"}, "strict_sni_host": true, "tls_connection_policies": []any{map[string]any{"match": map[string]any{"sni": []string{site.Hostname}}, "protocol_min": "tls1.3", "certificate_selection": map[string]any{"any_tag": []string{"site-" + packageIdentity(site.ApplicationID)}}}, map[string]any{"drop": true}}}
		if err := mergeSiteServer(servers, "site-"+packageIdentity(site.ApplicationID)+"-tls", https); err != nil {
			return nil, err
		}
		if site.PublicListen != "" {
			public := map[string]any{}
			for key, value := range https {
				public[key] = value
			}
			listen := site.PublicListen
			if container {
				listen = "0.0.0.0:443"
			}
			public["listen"] = []string{listen}
			if err := mergeSiteServer(servers, "site-"+packageIdentity(site.ApplicationID)+"-public", public); err != nil {
				return nil, err
			}
		}
		certificates := caddyObject(caddyObject(caddyObject(config, "apps"), "tls"), "certificates")
		pairs, _ := certificates["load_pem"].([]any)
		pairs = append(pairs, map[string]any{"certificate": site.Certificate.CertificatePEM, "key": site.Certificate.PrivateKeyPEM, "tags": []string{"site-" + packageIdentity(site.ApplicationID)}})
		certificates["load_pem"] = pairs
	}
	return json.Marshal(config)
}

func mergeSiteServer(servers map[string]any, name string, candidate map[string]any) error {
	listen := caddyStringSlice(candidate["listen"])
	if len(listen) != 1 {
		return errors.New("agent: website must have one owned listener")
	}
	for _, value := range servers {
		server, _ := value.(map[string]any)
		addresses := caddyStringSlice(server["listen"])
		if len(addresses) != 1 || addresses[0] != listen[0] {
			continue
		}
		existingTLS, candidateTLS := server["tls_connection_policies"] != nil, candidate["tls_connection_policies"] != nil
		if existingTLS != candidateTLS {
			return errors.New("agent: shared website listener conflicts with another protocol")
		}
		routes, _ := server["routes"].([]any)
		additions, _ := candidate["routes"].([]any)
		server["routes"] = append(additions, routes...)
		if candidateTLS {
			policies, _ := server["tls_connection_policies"].([]any)
			newPolicies, _ := candidate["tls_connection_policies"].([]any)
			// Keep the existing policy order and the final rejection policy.
			server["tls_connection_policies"] = append(newPolicies[:1], policies...)
			server["strict_sni_host"] = true
		}
		return nil
	}
	routes, _ := candidate["routes"].([]any)
	complete := make(map[string]any, len(candidate))
	for key, value := range candidate {
		complete[key] = value
	}
	complete["routes"] = append(append([]any(nil), routes...), map[string]any{"handle": []any{map[string]any{"handler": "static_response", "status_code": 404}}, "terminal": true})
	servers[name] = complete
	return nil
}

// ReloadSites uses the same mutation lock and complete base snapshot as gateway
// updates. Certificates and unrelated routes cannot be lost to a page update.
func (driver *CaddyGatewayDriver) ReloadSites(ctx context.Context) error {
	driver.mutationMu.Lock()
	defer driver.mutationMu.Unlock()
	if driver.Sites == nil || driver.SiteRuntime == nil {
		return errors.New("agent: shared Caddy website runtime is unavailable")
	}
	desired, certificates := driver.CurrentConfiguration()
	if driver.SystemGateway != nil {
		services, err := driver.SystemGateway.ProtectedSystemServices(ctx)
		if err != nil {
			return err
		}
		if err := validateProtectedSystemRoutes(desired, services); err != nil {
			return err
		}
	}
	var raw []byte
	var err error
	if desired.Revision > 0 {
		raw, err = caddyConfiguration(desired, certificates, driver.AdminListen)
	} else {
		raw, err = json.Marshal(map[string]any{"admin": map[string]any{"listen": driver.AdminListen}, "apps": map[string]any{"http": map[string]any{"servers": map[string]any{}}}})
	}
	if err != nil {
		return err
	}
	sites, err := driver.Sites(ctx)
	if err != nil {
		return err
	}
	raw, err = composeCaddySites(raw, desired, sites, driver.SiteContainer)
	if err != nil {
		return err
	}
	if err := driver.SiteRuntime(ctx, desired, sites); err != nil {
		if errors.Is(err, errSharedCaddyRuntimeAbsent) && len(sites) == 0 && desired.Revision == 0 {
			return nil
		}
		return err
	}
	if err := driver.load(ctx, raw); err != nil {
		return errors.New("agent: shared Caddy rejected the website configuration")
	}
	if len(sites) == 0 && desired.Revision == 0 && driver.SiteIdle != nil {
		return driver.SiteIdle(ctx)
	}
	return nil
}
