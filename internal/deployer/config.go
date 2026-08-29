package deployer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/gatewayruntime"
	"github.com/petauron/vastora/internal/networking"
)

var dnsLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

const (
	centerCertificatePath = "/etc/caddy/system/center.crt"
	centerPrivateKeyPath  = "/etc/caddy/system/center.key"
)

func centerAliasCertificatePath(index int) string {
	return fmt.Sprintf("/etc/caddy/system/center-alias-%d.crt", index+1)
}
func centerAliasPrivateKeyPath(index int) string {
	return fmt.Sprintf("/etc/caddy/system/center-alias-%d.key", index+1)
}

type gatewayResolution struct {
	hostname  string
	addresses []netip.Addr
}

type gatewayDNSResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

func normalizePublicURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", errors.New("deployer: built-in services require an HTTPS URL without a path")
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return "", errors.New("deployer: built-in services must use the standard HTTPS port 443")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if !validDNSName(hostname) {
		return "", errors.New("deployer: built-in services require a valid DNS hostname")
	}
	return "https://" + hostname, nil
}

func validDNSName(value string) bool {
	if len(value) > 253 || !strings.Contains(value, ".") || net.ParseIP(value) != nil {
		return false
	}
	labels := strings.Split(value, ".")
	for _, label := range labels {
		if !dnsLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func renderHeadscaleConfig(endpoint string) []byte {
	return []byte(fmt.Sprintf(`server_url: %s
listen_addr: 0.0.0.0:8081
metrics_listen_addr: ""
grpc_listen_addr: 127.0.0.1:50443
grpc_allow_insecure: false
trusted_proxies:
  - 127.0.0.1/32
  - 10.0.0.0/8
  - 172.16.0.0/12
  - 192.168.0.0/16

noise:
  private_key_path: /var/lib/headscale/noise_private.key

prefixes:
  v4: 100.64.0.0/10

derp:
  server:
    enabled: true
    region_id: 999
    region_code: vastora
    region_name: Vastora Embedded DERP
    verify_clients: true
    stun_listen_addr: "0.0.0.0:3478"
    private_key_path: /var/lib/headscale/derp_server_private.key
    automatically_add_embedded_derp_region: true
  urls: []
  paths:
    - /etc/headscale/derp.yaml
  auto_update_enabled: false
  update_frequency: 24h

disable_check_updates: true

database:
  type: sqlite
  sqlite:
    path: /var/lib/headscale/db.sqlite
    write_ahead_log: true
    wal_autocheckpoint: 1000

dns:
  magic_dns: true
  base_domain: vastora.internal
  override_local_dns: true
  nameservers:
    global:
      - 1.1.1.1
      - 1.0.0.1
    split: {}
  search_domains: []
  extra_records_path: /var/lib/vastora-shared/headscale-extra-records.json

policy:
  mode: file
  path: /etc/headscale/policy.hujson

unix_socket: /var/run/headscale/headscale.sock
unix_socket_permission: "0770"

log:
  format: text
  level: info

logtail:
  enabled: false

auto_update:
  enabled: false

node:
  expiry: 0
`, endpoint))
}

func renderHeadscaleDERPMap() []byte {
	return []byte(`regions:
  998:
    regionid: 998
    regioncode: cloudflare-stun
    regionname: Cloudflare STUN
    nodes:
      - name: cloudflare-stun
        regionid: 998
        hostname: stun.cloudflare.com
        stunport: 3478
        stunonly: true
        derpport: 0
`)
}

func renderHeadscalePolicy() []byte {
	return []byte(`{
  "tagOwners": {
    "tag:vastora-agent": ["vastora@"],
    "tag:vastora-gateway": ["vastora@"],
    "tag:vastora-center": ["vastora@"]
  },
  "grants": [
    {
      "src": ["autogroup:member"],
      "dst": ["tag:vastora-gateway"],
      "ip": ["80,443"]
    },
    {
      "src": ["tag:vastora-gateway"],
      "dst": ["tag:vastora-agent"],
      "ip": ["*"]
    },
    {
      "src": ["tag:vastora-agent"],
      "dst": ["tag:vastora-center"],
      "ip": ["443"]
    }
  ]
}
`)
}

func gatewayBindAddresses(ctx context.Context, publicAddress, bindAddress string, endpoints ...string) ([]string, error) {
	// The host resolver may intentionally return the local bind address for a
	// split-DNS hostname. Validate the public side of a NAT 1:1 mapping through
	// an independent resolver instead of rejecting that valid local view.
	publicResolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		transport := "udp4"
		if strings.HasPrefix(network, "tcp") {
			transport = "tcp4"
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, transport, "1.1.1.1:53")
	}}
	return gatewayBindAddressesWithResolver(ctx, publicResolver, publicAddress, bindAddress, endpoints...)
}

func gatewayBindAddressesWithResolver(ctx context.Context, resolver gatewayDNSResolver, publicAddress, bindAddress string, endpoints ...string) ([]string, error) {
	resolutions := make([]gatewayResolution, 0, len(endpoints))
	for _, endpoint := range endpoints {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Hostname() == "" {
			return nil, errors.New("deployer: gateway endpoint is invalid")
		}
		resolved, err := resolver.LookupNetIP(ctx, "ip", parsed.Hostname())
		if err != nil {
			return nil, fmt.Errorf("deployer: resolve public gateway hostname %s: %w", parsed.Hostname(), err)
		}
		resolutions = append(resolutions, gatewayResolution{hostname: parsed.Hostname(), addresses: resolved})
	}
	if strings.TrimSpace(publicAddress) == "" || strings.TrimSpace(bindAddress) == "" {
		return nil, errors.New("deployer: confirmed public and local gateway addresses are required")
	}
	parsedBind := net.ParseIP(strings.TrimSpace(bindAddress))
	candidates := []networking.Candidate{{Address: strings.TrimSpace(bindAddress), Kind: networking.Classify("configured", parsedBind)}}
	return selectMappedGatewayBindAddress(resolutions, candidates, publicAddress, bindAddress)
}

func selectMappedGatewayBindAddress(resolutions []gatewayResolution, candidates []networking.Candidate, publicValue, bindValue string) ([]string, error) {
	publicAddress, err := netip.ParseAddr(strings.TrimSpace(publicValue))
	if err != nil || networking.Classify("external", net.IP(publicAddress.AsSlice())) != networking.KindPublic {
		return nil, errors.New("deployer: a valid public gateway address is required")
	}
	publicAddress = publicAddress.Unmap()
	bindAddress, err := netip.ParseAddr(strings.TrimSpace(bindValue))
	if err != nil {
		return nil, errors.New("deployer: a valid local gateway bind address is required")
	}
	bindAddress = bindAddress.Unmap()
	if !bindAddress.Is4() {
		return nil, errors.New("deployer: gateway bind address must be IPv4")
	}
	bindFound := false
	for _, candidate := range candidates {
		candidateAddress, parseErr := netip.ParseAddr(candidate.Address)
		if parseErr == nil && candidateAddress.Unmap() == bindAddress && (candidate.Kind == networking.KindLAN || candidate.Kind == networking.KindPublic) {
			bindFound = true
			break
		}
	}
	if !bindFound {
		return nil, errors.New("deployer: gateway bind address must be assigned to this server")
	}
	for _, resolution := range resolutions {
		matched := false
		for _, address := range resolution.addresses {
			if address.Unmap() == publicAddress {
				matched = true
				break
			}
		}
		if !matched {
			return nil, fmt.Errorf("deployer: gateway hostname %s does not resolve to the confirmed public address", resolution.hostname)
		}
	}
	return formatGatewayBindAddresses([]netip.Addr{bindAddress}), nil
}

func formatGatewayBindAddresses(addresses []netip.Addr) []string {
	result := make([]string, 0, len(addresses))
	for _, address := range addresses {
		if address.Unmap().Is4() {
			result = append(result, address.String())
		}
	}
	return result
}

func centerPrivateBindAddresses(value string) ([]string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	requested, err := netip.ParseAddr(value)
	if err != nil {
		return nil, errors.New("deployer: Center private bind address is invalid")
	}
	requested = requested.Unmap()
	if !requested.Is4() {
		return nil, errors.New("deployer: Center private bind address must be IPv4")
	}
	if networking.Classify("tailscale0", net.IP(requested.AsSlice())) == networking.KindHeadscale {
		return formatGatewayBindAddresses([]netip.Addr{requested}), nil
	}
	return nil, errors.New("deployer: Center private bind address must be assigned to this server by Tailscale")
}

func renderCaddyfile(centerURL, centerOrigin, headscaleURL string, centerBindAddresses, bindAddresses []string, centerAliases []deployapi.CenterEndpointAlias, headscaleAliases []string) []byte {
	var result strings.Builder
	result.WriteString(fmt.Sprintf(`{
	admin unix/%s|0600
	persist_config off
}

`, gatewayruntime.CaddyAdminSocket))
	writeCenterGatewaySite(&result, centerURL, centerOrigin, centerCertificatePath, centerPrivateKeyPath, centerBindAddresses)
	for index, alias := range centerAliases {
		writeCenterGatewaySite(&result, alias.URL, centerOrigin, centerAliasCertificatePath(index), centerAliasPrivateKeyPath(index), centerBindAddresses)
	}
	writeHeadscaleGatewaySite(&result, headscaleURL, centerOrigin, bindAddresses)
	for _, alias := range headscaleAliases {
		writeHeadscaleGatewaySite(&result, alias, centerOrigin, bindAddresses)
	}
	return []byte(result.String())
}

func writeCenterGatewaySite(result *strings.Builder, endpoint, centerOrigin, certificatePath, privateKeyPath string, privateBindAddresses []string) {
	hostname := strings.TrimPrefix(endpoint, "https://")
	writeExplicitTLSSite(result, hostname, "system", centerOrigin, certificatePath, privateKeyPath)
	if len(privateBindAddresses) != 0 {
		writeExplicitTLSSite(result, hostname, "headscale", centerOrigin, certificatePath, privateKeyPath)
	}
}

func writeExplicitTLSSite(result *strings.Builder, hostname, listenerKind, upstream, certificatePath, privateKeyPath string) {
	httpPort, httpsPort, _ := gatewayruntime.CaddyListenerPorts(listenerKind)
	result.WriteString(fmt.Sprintf(`http://%s:%d {
	bind 0.0.0.0
	redir https://{host}{uri} 308
}

https://%s:%d {
	bind 0.0.0.0
	tls %s %s
	reverse_proxy %s
}

`, hostname, httpPort, hostname, httpsPort, certificatePath, privateKeyPath, upstream))
}

func writeHeadscaleGatewaySite(result *strings.Builder, endpoint, centerOrigin string, bindAddresses []string) {
	hostname := strings.TrimPrefix(endpoint, "https://")
	writeHeadscaleSite(result, hostname, "system", centerOrigin)
	if len(bindAddresses) != 0 {
		writeHeadscaleSite(result, hostname, "public", centerOrigin)
	}
}

func writeHeadscaleSite(result *strings.Builder, hostname, listenerKind, centerOrigin string) {
	httpPort, httpsPort, _ := gatewayruntime.CaddyListenerPorts(listenerKind)
	result.WriteString(fmt.Sprintf(`http://%s:%d {
	bind 0.0.0.0
	redir https://{host}{uri} 308
}

https://%s:%d {
	bind 0.0.0.0
	handle /install/agent.sh {
		reverse_proxy %s
	}
	handle /api/v1/agent-binaries/* {
		reverse_proxy %s
	}
	handle {
		reverse_proxy %s:8081
	}
}
`, hostname, httpPort, hostname, httpsPort, centerOrigin, centerOrigin, dockerruntime.HeadscaleAlias))
}
