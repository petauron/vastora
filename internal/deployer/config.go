package deployer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
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
  - ::1/128

noise:
  private_key_path: /var/lib/headscale/noise_private.key

prefixes:
  v4: 100.64.0.0/10
  v6: fd7a:115c:a1e0::/48

derp:
  server:
    enabled: false
    region_id: 999
    region_code: vastora
    region_name: Vastora Embedded DERP
    stun_listen_addr: "0.0.0.0:3478"
    private_key_path: /var/lib/headscale/derp_server_private.key
    automatically_add_embedded_derp_region: false
  urls:
    - https://controlplane.tailscale.com/derpmap/default
  paths: []
  auto_update_enabled: true
  update_frequency: 24h

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

node:
  expiry: 0
`, endpoint))
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
	candidates, err := networking.Discover(time.Now())
	if err != nil {
		return nil, fmt.Errorf("deployer: discover public gateway addresses: %w", err)
	}
	resolutions := make([]gatewayResolution, 0, len(endpoints))
	for _, endpoint := range endpoints {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Hostname() == "" {
			return nil, errors.New("deployer: gateway endpoint is invalid")
		}
		resolved, err := net.DefaultResolver.LookupNetIP(ctx, "ip", parsed.Hostname())
		if err != nil {
			return nil, fmt.Errorf("deployer: resolve gateway hostname %s: %w", parsed.Hostname(), err)
		}
		resolutions = append(resolutions, gatewayResolution{hostname: parsed.Hostname(), addresses: resolved})
	}
	if strings.TrimSpace(publicAddress) == "" && strings.TrimSpace(bindAddress) == "" {
		return selectGatewayBindAddresses(resolutions, candidates)
	}
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

func selectGatewayBindAddresses(resolutions []gatewayResolution, candidates []networking.Candidate) ([]string, error) {
	public := make(map[netip.Addr]struct{})
	for _, candidate := range candidates {
		if candidate.Kind == networking.KindPublic {
			if address, err := netip.ParseAddr(candidate.Address); err == nil {
				public[address.Unmap()] = struct{}{}
			}
		}
	}
	matched := make(map[netip.Addr]struct{})
	for _, resolution := range resolutions {
		hostMatched := false
		for _, address := range resolution.addresses {
			address = address.Unmap()
			if _, exists := public[address]; exists {
				matched[address] = struct{}{}
				hostMatched = true
			}
		}
		if !hostMatched {
			return nil, fmt.Errorf("deployer: gateway hostname %s must resolve directly to a public address assigned to this server", resolution.hostname)
		}
	}
	addresses := make([]netip.Addr, 0, len(matched))
	for address := range matched {
		addresses = append(addresses, address)
	}
	sort.Slice(addresses, func(left, right int) bool { return addresses[left].Compare(addresses[right]) < 0 })
	return formatGatewayBindAddresses(addresses), nil
}

func formatGatewayBindAddresses(addresses []netip.Addr) []string {
	result := make([]string, 0, len(addresses))
	for _, address := range addresses {
		if address.Is6() {
			result = append(result, "["+address.String()+"]")
		} else {
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
	candidates, err := networking.Discover(time.Now())
	if err != nil {
		return nil, fmt.Errorf("deployer: discover Center private address: %w", err)
	}
	for _, candidate := range candidates {
		address, parseErr := netip.ParseAddr(candidate.Address)
		if parseErr == nil && address.Unmap() == requested && candidate.Kind == networking.KindHeadscale {
			return formatGatewayBindAddresses([]netip.Addr{requested}), nil
		}
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
	httpEndpoint := "http://" + strings.TrimPrefix(endpoint, "https://")
	addresses := strings.Join(append([]string{"127.0.0.1"}, privateBindAddresses...), " ")
	result.WriteString(fmt.Sprintf(`%s {
	bind %s
	redir https://{host}{uri} 308
}

%s {
	bind %s
	tls %s %s
	reverse_proxy %s
}

`, httpEndpoint, addresses, endpoint, addresses, certificatePath, privateKeyPath, centerOrigin))
}

func writeHeadscaleGatewaySite(result *strings.Builder, endpoint, centerOrigin string, bindAddresses []string) {
	httpEndpoint := "http://" + strings.TrimPrefix(endpoint, "https://")
	addresses := strings.Join(bindAddresses, " ")
	result.WriteString(fmt.Sprintf(`%s {
	bind 127.0.0.1 %s
	redir https://{host}{uri} 308
}

%s {
	bind 127.0.0.1 %s
	handle /install/agent.sh {
		reverse_proxy %s
	}
	handle /api/v1/agent-binaries/* {
		reverse_proxy %s
	}
	handle {
		reverse_proxy 127.0.0.1:8081
	}
}
`, httpEndpoint, addresses, endpoint, addresses, centerOrigin, centerOrigin))
}
