package deployer

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

var dnsLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func normalizePublicURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", errors.New("deployer: built-in services require an HTTPS URL without a path")
	}
	if parsed.Port() != "8443" {
		return "", errors.New("deployer: built-in services must use HTTPS port 8443 so application port 443 remains free")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if !validDNSName(hostname) {
		return "", errors.New("deployer: built-in services require a valid DNS hostname")
	}
	return "https://" + hostname + ":8443", nil
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
listen_addr: 127.0.0.1:8081
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
      "ip": ["443,8443"]
    }
  ]
}
`)
}

func renderCaddyfile(centerURL, centerOrigin, headscaleURL string) []byte {
	return []byte(fmt.Sprintf(`{
	admin off
	persist_config off
}

%s {
	reverse_proxy %s
}

%s {
	reverse_proxy 127.0.0.1:8081
}
`, centerURL, centerOrigin, headscaleURL))
}
