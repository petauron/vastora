package landing

import "net/netip"

// Shared by reported-exit validation and the landing firewall. These networks
// must never become proxy destinations, including after DNS resolution. IPv6
// requires a global-unicast destination and excludes transition networks.
var blockedIPv4 = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
	"192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
	"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
}

// PublicIPv4 accepts only an IPv4 address that can be a public landing exit.
func PublicIPv4(address netip.Addr) bool {
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

// Transition, documentation and special-purpose prefixes must not bridge the
// IPv6 destination boundary into private IPv4 or local infrastructure.
var blockedIPv6 = []string{"2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"}

func PublicIPv6(address netip.Addr) bool {
	if !address.Is6() || address.Is4In6() || address.Zone() != "" || !netip.MustParsePrefix("2000::/3").Contains(address) {
		return false
	}
	for _, value := range blockedIPv6 {
		if netip.MustParsePrefix(value).Contains(address) {
			return false
		}
	}
	return true
}

func PublicIP(address netip.Addr) bool { return PublicIPv4(address) || PublicIPv6(address) }

// A NAT host may bind its RFC1918 interface. Mesh, loopback, wildcard and
// link-local addresses are never eligible as a landing source address.
func ValidEgressIP(value string) bool {
	address, err := netip.ParseAddr(value)
	if err != nil || address.String() != value {
		return false
	}
	return PublicIP(address) || address.Is4() && address.IsPrivate()
}
