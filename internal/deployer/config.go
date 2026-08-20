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

	"github.com/petauron/vastora/internal/networking"
)

var dnsLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

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
  override_local_dns: false
  nameservers:
    global: []
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

func gatewayBindAddresses(ctx context.Context, endpoints ...string) ([]string, error) {
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
	return selectGatewayBindAddresses(resolutions, candidates)
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
	result := []string{"127.0.0.1"}
	for _, address := range addresses {
		if address.Is6() {
			result = append(result, "["+address.String()+"]")
		} else {
			result = append(result, address.String())
		}
	}
	return result, nil
}

func renderCaddyfile(centerURL, centerOrigin, headscaleURL string, bindAddresses []string) []byte {
	centerHTTP := "http://" + strings.TrimPrefix(centerURL, "https://")
	headscaleHTTP := "http://" + strings.TrimPrefix(headscaleURL, "https://")
	return []byte(fmt.Sprintf(`{
	admin off
	persist_config off
	default_bind %s
}

%s, %s {
	redir https://{host}{uri} 308
}

%s {
	reverse_proxy %s
}

%s {
	reverse_proxy 127.0.0.1:8081
}
`, strings.Join(bindAddresses, " "), centerHTTP, headscaleHTTP, centerURL, centerOrigin, headscaleURL))
}
