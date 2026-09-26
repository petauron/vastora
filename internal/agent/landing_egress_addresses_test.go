package agent

import (
	"net"
	"testing"
)

func TestLandingEgressAddressesIncludeIPv6AndExcludeNonEgressInterfaces(t *testing.T) {
	interfaces := []net.Interface{{Name: "eth0", Flags: net.FlagUp}, {Name: "eth1"}, {Name: "lo", Flags: net.FlagUp | net.FlagLoopback}, {Name: "docker0", Flags: net.FlagUp}, {Name: "tailscale0", Flags: net.FlagUp}}
	result, err := landingEgressAddresses(interfaces, func(iface net.Interface) ([]net.Addr, error) {
		if iface.Name != "eth0" {
			t.Fatalf("enumerated excluded interface %s", iface.Name)
		}
		values := []string{"10.0.0.2/24", "2001:4860:4860::8888/64", "2001:4860:4860::8844/128", "fe80::1/64", "fd00::1/64", "::1/128", "2001:db8::1/64"}
		var out []net.Addr
		for _, value := range values {
			ip, network, parseErr := net.ParseCIDR(value)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			network.IP = ip
			out = append(out, network)
		}
		return out, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 3 || result[0].Address != "10.0.0.2" || result[1].Address != "2001:4860:4860::8844" || result[2].Address != "2001:4860:4860::8888" {
		t.Fatalf("unexpected inventory: %+v", result)
	}
}
