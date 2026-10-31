package osreinstall

import (
	"strings"
	"testing"
)

const testStaticNetplan = `network:
  version: 2
  ethernets:
    eth0:
      addresses: [192.0.2.10/24]
      gateway4: 192.0.2.1
      match:
        macaddress: 02:00:00:00:00:01
      set-name: eth0
      nameservers:
        addresses: [192.0.2.53]
        search: [example.test]
`

func staticNetworkEvidence() Network {
	return Network{Interface: "eth0", MAC: "02:00:00:00:00:01", Addresses: []Address{{Prefix: "192.0.2.10/24"}}, Gateways: []Gateway{{Family: "ipv4", Address: "192.0.2.1"}}, DNS: []string{"192.0.2.53"}}
}

func TestParseStaticNetplanPreservesExplicitSettings(t *testing.T) {
	resolver := []byte("# generated\nnameserver 192.0.2.53\nsearch example.test\n")
	got, err := ParseStaticNetplan([]byte(testStaticNetplan), resolver, staticNetworkEvidence())
	if err != nil {
		t.Fatal(err)
	}
	if got.Address != "192.0.2.10/24" || got.Gateway != "192.0.2.1" || len(got.Search) != 1 || got.Search[0] != "example.test" || string(got.Resolver) != string(resolver) {
		t.Fatalf("network settings changed: %+v", got)
	}
	resolver[0] = 'x'
	if got.Resolver[0] != '#' {
		t.Fatal("resolver was not copied")
	}
}

func TestParseStaticNetplanRefusesUnsafeInference(t *testing.T) {
	for name, change := range map[string]func(string) string{
		"dhcp": func(s string) string {
			return strings.Replace(s, "addresses: [192.0.2.10/24]", "dhcp4: true\n      addresses: [192.0.2.10/24]", 1)
		},
		"route": func(s string) string {
			return strings.Replace(s, "gateway4: 192.0.2.1", "gateway4: 192.0.2.1\n      routes: [{to: 0.0.0.0/0, via: 192.0.2.1}]", 1)
		},
		"different gateway":  func(s string) string { return strings.Replace(s, "gateway4: 192.0.2.1", "gateway4: 192.0.2.2", 1) },
		"different MAC":      func(s string) string { return strings.Replace(s, "02:00:00:00:00:01", "02:00:00:00:00:02", 1) },
		"multiple documents": func(s string) string { return s + "---\nnetwork: {}\n" },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseStaticNetplan([]byte(change(testStaticNetplan)), []byte("nameserver 192.0.2.53\nsearch example.test\n"), staticNetworkEvidence()); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
	if _, err := ParseStaticNetplan([]byte(testStaticNetplan), []byte("nameserver 127.0.0.53\n"), staticNetworkEvidence()); err == nil {
		t.Fatal("local resolver accepted")
	}
	dual := staticNetworkEvidence()
	dual.Addresses = append(dual.Addresses, Address{Prefix: "2001:db8::10/64", Dynamic: true})
	if _, err := ParseStaticNetplan([]byte(strings.Replace(testStaticNetplan, "gateway4: 192.0.2.1", "gateway4: 192.0.2.1\n      accept-ra: false", 1)), []byte("nameserver 192.0.2.53\nsearch example.test\n"), dual); err == nil {
		t.Fatal("dual stack inference accepted")
	}
}

func TestRenderStaticNetworkRetainsNameAndResolver(t *testing.T) {
	resolver := []byte("nameserver 192.0.2.53\nsearch example.test\n")
	plan, err := ParseStaticNetplan([]byte(testStaticNetplan), resolver, staticNetworkEvidence())
	if err != nil {
		t.Fatal(err)
	}
	link, interfaces, restoredResolver, err := plan.RenderStaticNetwork()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(link), `ATTR{address}=="02:00:00:00:00:01"`) || !strings.Contains(string(link), `NAME="eth0"`) || !strings.Contains(string(interfaces), "iface eth0 inet static\n") || !strings.Contains(string(interfaces), "address 192.0.2.10/24\n") || string(restoredResolver) != string(resolver) {
		t.Fatal("network restoration did not preserve the reviewed settings")
	}
	if _, err := ParseStaticInterfaces(interfaces, restoredResolver, staticNetworkEvidence()); err != nil {
		t.Fatalf("rendered network configuration cannot be re-inspected: %v", err)
	}
	if _, err := ParseStaticInterfaces([]byte(strings.Replace(string(interfaces), "dns-search example.test", "dns-search other.test", 1)), restoredResolver, staticNetworkEvidence()); err == nil {
		t.Fatal("conflicting DNS search domains accepted")
	}
	plan.Interface = "eth0\nName=other"
	if _, _, _, err := plan.RenderStaticNetwork(); err == nil {
		t.Fatal("unsafe interface name accepted")
	}
}

func TestParseStaticInterfacesPreservesSettingsAndRejectsExtraRoutes(t *testing.T) {
	config := []byte("source /etc/network/interfaces.d/*\n\nauto lo\niface lo inet loopback\n\nauto eth0\niface eth0 inet static\n    address 192.0.2.10/24\n    gateway 192.0.2.1\n    dns-nameservers 192.0.2.53\n")
	resolver := []byte("nameserver 192.0.2.53\nsearch example.test\n")
	plan, err := ParseStaticInterfaces(config, resolver, staticNetworkEvidence())
	if err != nil || plan.Interface != "eth0" || plan.Gateway != "192.0.2.1" || len(plan.Search) != 1 || plan.Search[0] != "example.test" {
		t.Fatalf("static settings not preserved: %+v %v", plan, err)
	}
	if _, err := ParseStaticInterfaces(append(config, []byte("    post-up ip route add 198.51.100.0/24 via 192.0.2.2\n")...), resolver, staticNetworkEvidence()); err == nil {
		t.Fatal("unreviewed routing command accepted")
	}
}

func TestParseStaticNetplanDefaultRoute(t *testing.T) {
	config := strings.Replace(testStaticNetplan, "gateway4: 192.0.2.1", "routes: [{to: default, via: 192.0.2.1}]", 1)
	resolver := []byte("nameserver 192.0.2.53\nsearch example.test\n")
	if _, err := ParseStaticNetplan([]byte(config), resolver, staticNetworkEvidence()); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		strings.Replace(config, "via: 192.0.2.1", "via: 192.0.2.2", 1),
		strings.Replace(config, "to: default", "to: 192.0.2.0/24", 1),
		strings.Replace(config, "via: 192.0.2.1", "via: 192.0.2.1, metric: 5", 1),
	} {
		if _, err := ParseStaticNetplan([]byte(invalid), resolver, staticNetworkEvidence()); err == nil {
			t.Fatal("unreviewed route accepted")
		}
	}
}
