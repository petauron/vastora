package landing

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

const (
	UDPRelayFirst = 1081
	UDPRelayLast  = 1208
)

type AuthorizedNode struct {
	Address string `json:"address"`
	TCPOnly bool   `json:"tcpOnly,omitempty"`
}

// Exact private source membership is the authentication boundary. Center
// generates this plan from managed nodes; no SOCKS password is needed.
type ServerPlan struct {
	EgressIP string           `json:"egressIp,omitempty"`
	Revision uint64           `json:"revision"`
	Address  string           `json:"address"`
	Sources  []AuthorizedNode `json:"sources"`
}

func (plan ServerPlan) Validate() error {
	if plan.EgressIP != "" && !ValidEgressIP(plan.EgressIP) {
		return errors.New("landing: invalid selected egress IP")
	}
	if plan.Revision == 0 || !tailnetIPv4(plan.Address) || len(plan.Sources) > 128 {
		return errors.New("landing: invalid native server plan")
	}
	addresses := map[string]bool{}
	for _, node := range plan.Sources {
		if strings.Contains(plan.EgressIP, ":") && !node.TCPOnly {
			return errors.New("landing: IPv6 egress requires TCP-only source grants")
		}
		if !tailnetIPv4(node.Address) || node.Address == plan.Address || addresses[node.Address] {
			return errors.New("landing: invalid authorized proxy node")
		}
		addresses[node.Address] = true
	}
	return nil
}

func tailnetIPv4(value string) bool {
	address, err := netip.ParseAddr(value)
	return err == nil && address.String() == value && netip.MustParsePrefix("100.64.0.0/10").Contains(address)
}

func validUDPRelay(value, peerAddress string) bool {
	endpoint, err := netip.ParseAddrPort(value)
	return err == nil && endpoint.Addr().String() == peerAddress && endpoint.Port() >= UDPRelayFirst && endpoint.Port() <= UDPRelayLast
}

// Egress is observed by Agent and may be RFC1918 on a NAT VPS, but never a
// wildcard or a tailnet address. DNS runs on the landing host. UID-scoped
// filtering supplies the post-resolution destination boundary.
func (plan ServerPlan) RenderDante(egress string) ([]byte, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	address, err := netip.ParseAddr(egress)
	if err != nil || !ValidEgressIP(egress) || plan.EgressIP != "" && plan.EgressIP != egress || plan.EgressIP == "" && !address.Is4() {
		return nil, errors.New("landing: invalid observed egress address")
	}
	family, destination, blocked := "ipv4", "0.0.0.0/0", blockedIPv4
	if address.Is6() {
		family, destination, blocked = "ipv6", "::/0", blockedIPv6
	}
	var config strings.Builder
	fmt.Fprintf(&config, `# Managed by Vastora
internal.protocol: ipv4
internal: %s port = %d
external.protocol: %s
external: %s
clientmethod: none
socksmethod: none
user.privileged: vastora-landing
user.unprivileged: vastora-landing
timeout.negotiate: 10
timeout.connect: 10
timeout.io.tcp: 300
timeout.io.udp: 60
`, plan.Address, SOCKSPort, family, egress)
	for _, source := range plan.Sources {
		fmt.Fprintf(&config, "client pass {\n from: %s/32 to: %s/32\n}\n", source.Address, plan.Address)
	}
	config.WriteString("client block {\n from: 0.0.0.0/0 to: 0.0.0.0/0\n}\n")
	// UDP ASSOCIATE's 0.0.0.0:0 describes the client, not an egress
	// destination. The kernel policy filters every actual UDP destination.
	for _, cidr := range blocked {
		fmt.Fprintf(&config, "socks block {\n from: 0.0.0.0/0 to: %s\n command: connect\n}\n", cidr)
	}
	for _, port := range strings.Split(managementPorts, ",") {
		fmt.Fprintf(&config, "socks block {\n from: 0.0.0.0/0 to: %s port = %s\n command: connect\n}\n", destination, port)
	}
	for _, source := range plan.Sources {
		if source.TCPOnly {
			fmt.Fprintf(&config, "socks pass {\n from: %s/32 to: %s\n command: connect\n protocol: tcp\n}\n", source.Address, destination)
			continue
		}
		fmt.Fprintf(&config, "socks pass {\n from: %s/32 to: 0.0.0.0/0\n command: connect udpassociate\n protocol: tcp udp\n udp.portrange: %d-%d\n}\n", source.Address, UDPRelayFirst, UDPRelayLast)
		// Dante evaluates UDP replies in the reverse direction. Restrict the
		// recipient to the same authorized client; this does not grant another
		// source permission to establish a SOCKS association. The UID firewall
		// still requires replies to an established private client flow.
		fmt.Fprintf(&config, "socks pass {\n from: 0.0.0.0/0 to: %s/32\n command: udpreply\n protocol: udp\n}\n", source.Address)
	}
	config.WriteString("socks block {\n from: 0.0.0.0/0 to: 0.0.0.0/0\n}\n")
	return []byte(config.String()), nil
}

const managementPorts = "22,23,135,139,445,1080,1433,1521,2053,2375,2376,3306,3389,5432,5900,6379,6443,8006,9000,9090,9100,10000"

const NativeResolver = "nameserver 1.1.1.1\nnameserver 1.0.0.1\noptions timeout:2 attempts:2\n"
const NativeNSS = "passwd: files\ngroup: files\nshadow: files\nhosts: files dns\nnetworks: files\nservices: files\nprotocols: files\n"

const NativeUnit = `[Unit]
Description=Vastora landing SOCKS service
Requires=vastora-landing-firewall.service
After=network-online.target tailscaled.service vastora-landing-firewall.service
Wants=network-online.target

[Service]
Type=simple
User=vastora-landing
Group=vastora-landing
RuntimeDirectory=vastora-landing
RuntimeDirectoryMode=0700
Environment=TMPDIR=/run/vastora-landing
ExecStart=/etc/vastora-landing/danted -f /etc/vastora-landing/danted.conf -p /run/vastora-landing/danted.pid
Restart=on-failure
RestartSec=5
TimeoutStopSec=10
KillMode=control-group
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
BindReadOnlyPaths=/etc/vastora-landing/resolv.conf:/etc/resolv.conf
BindReadOnlyPaths=/etc/vastora-landing/nsswitch.conf:/etc/nsswitch.conf
# Tailscale may replace the host resolver mount after this unit starts.
# Keep its unprivileged DNS resolver pinned for the lifetime of Dante.
MountFlags=private
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
# Dante uses getifaddrs via unprivileged netlink to locate the bound IPv6 address.
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
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
