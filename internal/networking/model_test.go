package networking

import (
	"net"
	"testing"
)

func TestClassifyUsesOnlyLocallyAssignedAddressProperties(t *testing.T) {
	tests := []struct {
		name          string
		interfaceName string
		address       string
		want          string
	}{
		{name: "LAN", interfaceName: "eth0", address: "192.168.1.20", want: KindLAN},
		{name: "CGNAT is not public", interfaceName: "eth0", address: "100.100.10.20", want: KindLAN},
		{name: "Tailscale", interfaceName: "tailscale0", address: "100.64.0.20", want: KindHeadscale},
		{name: "Docker bridge ignored", interfaceName: "docker0", address: "172.17.0.1", want: ""},
		{name: "Docker network bridge ignored", interfaceName: "br-24fc51862a30", address: "172.18.0.1", want: ""},
		{name: "container interface ignored", interfaceName: "veth1234", address: "172.19.0.1", want: ""},
		{name: "WireGuard overlay ignored", interfaceName: "wg0", address: "10.77.0.6", want: ""},
		{name: "ordinary LAN bridge retained", interfaceName: "br0", address: "10.0.0.1", want: KindLAN},
		{name: "public IPv4", interfaceName: "eth0", address: "203.0.113.20", want: KindPublic},
		{name: "IPv6 ignored", interfaceName: "tailscale0", address: "fd7a:115c:a1e0::1", want: ""},
		{name: "loopback ignored", interfaceName: "lo0", address: "127.0.0.1", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Classify(test.interfaceName, net.ParseIP(test.address)); got != test.want {
				t.Fatalf("Classify(%q, %q) = %q, want %q", test.interfaceName, test.address, got, test.want)
			}
		})
	}
}

func TestValidateProfileRequiresConfirmedLocalPublicAddress(t *testing.T) {
	candidates := []Candidate{
		{Address: "10.0.0.2", Interface: "eth0", Kind: KindLAN},
		{Address: "203.0.113.2", Interface: "eth1", Kind: KindPublic},
	}
	valid := Profile{ServiceAddress: "10.0.0.2", LANAddress: "10.0.0.2", PublicAddress: "203.0.113.2", EnabledKinds: []string{KindLAN, KindPublic}, DirectPublic: true}
	if err := ValidateProfile(candidates, valid); err != nil {
		t.Fatalf("valid profile was rejected: %v", err)
	}
	invalid := valid
	invalid.PublicAddress = "198.51.100.9"
	if err := ValidateProfile(candidates, invalid); err == nil {
		t.Fatal("unreported public address was accepted")
	}
}
