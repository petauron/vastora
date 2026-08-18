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
		{name: "public IPv4", interfaceName: "eth0", address: "203.0.113.20", want: KindPublic},
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
		{Address: "10.0.0.2", Interface: "eth0", Family: "ipv4", Kind: KindLAN},
		{Address: "203.0.113.2", Interface: "eth1", Family: "ipv4", Kind: KindPublic},
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
