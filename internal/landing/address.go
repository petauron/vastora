package landing

import "net/netip"

// Shared by reported-exit validation and the landing firewall. These networks
// must never become proxy destinations, including after DNS resolution. IPv6
// is deliberately unsupported in the first landing implementation.
var blockedIPv4 = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
	"192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
	"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
}

func publicIPv4(address netip.Addr) bool {
	if !address.Is4() || !address.IsGlobalUnicast() {
		return false
	}
	for _, value := range blockedIPv4 {
		if netip.MustParsePrefix(value).Contains(address) {
			return false
		}
	}
	return true
}
