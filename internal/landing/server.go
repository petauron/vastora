package landing

import (
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
)

const XrayVersion = "26.3.27"

type AuthorizedNode struct {
	Address     string      `json:"address"`
	Credentials Credentials `json:"credentials"`
}

// ServerPlan is private, encrypted command data. Public API responses expose
// node IDs and state, never this object or the rendered Xray configuration.
type ServerPlan struct {
	Revision uint64           `json:"revision"`
	Address  string           `json:"address"`
	Sources  []AuthorizedNode `json:"sources"`
}

func (plan ServerPlan) Validate() error {
	if plan.Revision == 0 || !tailnetIPv4(plan.Address) || len(plan.Sources) == 0 || len(plan.Sources) > 128 {
		return errors.New("landing: invalid native server plan")
	}
	addresses, accounts := map[string]bool{}, map[string]bool{}
	for _, node := range plan.Sources {
		if !tailnetIPv4(node.Address) || node.Address == plan.Address || addresses[node.Address] || !node.Credentials.valid() || accounts[node.Credentials.Username] {
			return errors.New("landing: invalid authorized proxy node")
		}
		addresses[node.Address], accounts[node.Credentials.Username] = true, true
	}
	return nil
}

func tailnetIPv4(value string) bool {
	address, err := netip.ParseAddr(value)
	return err == nil && address.String() == value && netip.MustParsePrefix("100.64.0.0/10").Contains(address)
}

// RenderXray uses the pinned release's accounts/auth/ip schema, not the latest
// documentation's users field. ForceIPv4 never falls back to system DNS or to
// an IPv6 connection. The native service's UID-scoped kernel policy must also
// be installed before startup: Xray routing alone cannot close DNS rebinding.
func (plan ServerPlan) RenderXray() ([]byte, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	accounts, sources := []any{}, []string{}
	for _, node := range plan.Sources {
		accounts = append(accounts, map[string]string{"user": node.Credentials.Username, "pass": node.Credentials.Password})
		sources = append(sources, node.Address)
	}
	denied := append(slices.Clone(blockedIPv4), "::/0")
	config := map[string]any{
		"log": map[string]any{"loglevel": "none", "access": "none", "error": "none"},
		"dns": map[string]any{"tag": "vastora-landing-dns", "servers": []string{"https://1.1.1.1/dns-query", "https://1.0.0.1/dns-query"}, "queryStrategy": "UseIPv4", "disableFallback": true},
		"inbounds": []any{map[string]any{
			"tag": "vastora-landing-socks", "listen": plan.Address, "port": SOCKSPort, "protocol": "socks",
			"settings": map[string]any{"auth": "password", "accounts": accounts, "udp": true, "ip": plan.Address, "userLevel": 0},
			"sniffing": map[string]any{"enabled": false},
		}},
		"outbounds": []any{
			map[string]any{"tag": "blocked", "protocol": "blackhole", "settings": map[string]any{}},
			map[string]any{"tag": "public-ipv4", "protocol": "freedom", "settings": map[string]any{"domainStrategy": "ForceIPv4"}},
		},
		"routing": map[string]any{"domainStrategy": "IPOnDemand", "rules": []any{
			map[string]any{"type": "field", "ip": denied, "outboundTag": "blocked"},
			map[string]any{"type": "field", "port": managementPorts, "outboundTag": "blocked"},
			map[string]any{"type": "field", "inboundTag": []string{"vastora-landing-dns"}, "ip": []string{"1.1.1.1", "1.0.0.1"}, "port": "443", "network": "tcp", "outboundTag": "public-ipv4"},
			map[string]any{"type": "field", "source": sources, "inboundTag": []string{"vastora-landing-socks"}, "network": "tcp,udp", "outboundTag": "public-ipv4"},
		}},
		"policy": map[string]any{"levels": map[string]any{"0": map[string]any{"handshake": 4, "connIdle": 120, "uplinkOnly": 2, "downlinkOnly": 5, "bufferSize": 16}}},
	}
	return json.MarshalIndent(config, "", "  ")
}

// Includes the platform's management plane and common administrative services.
// Public web destinations remain usable; this is not a blanket port-443 deny.
const managementPorts = "22,23,135,139,445,1080,1433,1521,2053,2375,2376,3306,3389,5432,5900,6379,6443,8006,9000,9090,9100,10000"

const NativeUnit = `[Unit]
Description=Vastora landing SOCKS service
Requires=vastora-landing-firewall.service
After=network-online.target tailscaled.service vastora-landing-firewall.service
Wants=network-online.target

[Service]
Type=simple
User=vastora-landing
Group=vastora-landing
LoadCredential=xray.json:/etc/vastora-landing/xray.json
ExecStart=/opt/vastora-landing/xray run -config %d/xray.json
Restart=on-failure
RestartSec=5
TimeoutStopSec=10
KillMode=control-group
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictAddressFamilies=AF_INET AF_UNIX
CapabilityBoundingSet=
AmbientCapabilities=
UMask=0077
MemoryMax=128M
TasksMax=64
LimitNOFILE=4096
StandardOutput=null
StandardError=null

[Install]
WantedBy=multi-user.target
`
