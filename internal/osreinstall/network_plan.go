package osreinstall

import "strings"

// StaticNetworkPlan records declared physical network policies for restoration.
// Observed leases are retained as installation evidence, never rendered static.
// Workload and disk protections are verified separately.
type StaticNetworkPlan struct {
	Interface      string
	MAC            string
	Address        string
	Gateway        string
	DNS            []string
	Search         []string
	Resolver       []byte
	IPv6Autoconf   string
	IPv6AcceptRA   string
	IPv6           []string
	Gateway6       string
	GatewayOnLink  bool
	Gateway6OnLink bool
	DHCP4          bool
	DynamicDNS     bool
	DHCP6          bool
	DirectRoutes   []string
	MTU            int
	Additional     []StaticNetworkPlan
}

func validSearchDomain(value string) bool {
	if len(value) == 0 || len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
