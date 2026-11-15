package osreinstall

import (
	"strings"
	"testing"
)

func TestNetworkRoundTripOnlinkIPv6AndSecondNIC(t *testing.T) {
	resolver := []byte("nameserver 192.0.2.53\n")
	plan := StaticNetworkPlan{Interface: "eth0", MAC: "02:00:00:00:00:01", Address: "192.0.2.10/32", Gateway: "192.0.2.1", GatewayOnLink: true, IPv6: []string{"2001:db8::10/128", "2001:db8:1::10/64"}, Gateway6: "2001:db8::1", Gateway6OnLink: true, IPv6AcceptRA: "1", DNS: []string{"192.0.2.53"}, Resolver: resolver, Additional: []StaticNetworkPlan{{Interface: "eth1", MAC: "02:00:00:00:00:02", Address: "198.51.100.10/24", IPv6AcceptRA: "0"}}}
	observed := Network{Interface: plan.Interface, MAC: plan.MAC, Addresses: []Address{{Prefix: plan.Address}, {Prefix: plan.IPv6[0]}, {Prefix: plan.IPv6[1]}, {Prefix: "2001:db8:2::10/64", Dynamic: true}}, Gateways: []Gateway{{Family: "ipv4", Address: plan.Gateway, OnLink: true}, {Family: "ipv6", Address: plan.Gateway6, OnLink: true}, {Family: "ipv6", Address: "fe80::1", Dynamic: true}}, DNS: plan.DNS, Additional: []Network{{Interface: "eth1", MAC: "02:00:00:00:00:02", Addresses: []Address{{Prefix: "198.51.100.10/24"}}}}}
	rules, config, res, e := plan.RenderStaticNetwork()
	if e != nil {
		t.Fatal(e)
	}
	if strings.Count(string(rules), "SUBSYSTEM") == 0 || !strings.Contains(string(config), "onlink") || strings.Contains(string(config), "2001:db8:2::10") {
		t.Fatal("lease or MAC binding rendered incorrectly")
	}
	got, e := ParseStaticInterfaces(config, res, observed)
	if e != nil {
		t.Fatal(e)
	}
	if !sameNetworkPlan(plan, got) {
		t.Fatalf("restored policies differ: %#v %#v", plan, got)
	}
	got.GatewayOnLink = false
	if _, _, _, e = got.RenderStaticNetwork(); e == nil {
		t.Fatal("off-subnet gateway without onlink accepted")
	}
	changed := observed
	changed.Additional = []Network{{Interface: "eth1", MAC: "02:00:00:00:00:03", Addresses: observed.Additional[0].Addresses}}
	if matchPlanObservation(plan, changed) == nil {
		t.Fatal("changed secondary NIC accepted")
	}
}
func TestNetworkDHCPDoesNotFreezeLease(t *testing.T) {
	observed := staticNetworkEvidence()
	observed.Addresses[0].Dynamic = true
	observed.Gateways[0].Dynamic = true
	config := strings.Replace(testStaticNetplan, "addresses: [192.0.2.10/24]\n      gateway4: 192.0.2.1", "dhcp4: true", 1)
	p, e := ParseStaticNetplan([]byte(config), []byte("nameserver 192.0.2.53\nsearch example.test\n"), observed)
	if e != nil {
		t.Fatal(e)
	}
	_, generated, _, e := p.RenderStaticNetwork()
	if e != nil || !strings.Contains(string(generated), "iface eth0 inet dhcp") || strings.Contains(string(generated), "address 192.0.2.10") {
		t.Fatalf("lease was frozen: %v", e)
	}
	p2 := p
	p2.Address = "192.0.2.20/24"
	p2.Gateway = "192.0.2.254"
	if !sameNetworkPlan(p, p2) {
		t.Fatal("valid DHCP renewal treated as static mismatch")
	}
	p2.DHCP4 = false
	if sameNetworkPlan(p, p2) {
		t.Fatal("DHCP disabled without detection")
	}
}
func TestNetworkUnrecognizedSettingsFailClosed(t *testing.T) {
	original := staticNetworkEvidence()
	resolver := []byte("nameserver 192.0.2.53\nsearch example.test\n")
	for _, extra := range []string{"      routing-policy: [{from: 192.0.2.10, table: 100}]\n", "      routes: [{to: 203.0.113.0/24, via: 192.0.2.1}]\n", "      mtu: 40\n"} {
		if _, e := ParseStaticNetplan([]byte(testStaticNetplan+extra), resolver, original); e == nil {
			t.Fatal("unsupported network semantics accepted")
		}
	}
	if _, e := ParseStaticNetplan([]byte(strings.Replace(testStaticNetplan, "192.0.2.53", "192.0.2.54", 1)), resolver, original); e == nil {
		t.Fatal("DNS policy mismatch accepted")
	}
}

func TestDeclaredRouteWithoutProtocolField(t *testing.T) {
	p := StaticNetworkPlan{Interface: "eth0", MAC: "02:00:00:00:00:01", Address: "192.0.2.10/32", Gateway: "192.0.2.1", GatewayOnLink: true, DirectRoutes: []string{"192.0.2.53/32"}}
	n := Network{Interface: "eth0", MAC: p.MAC, Addresses: []Address{{Prefix: p.Address}}, Gateways: []Gateway{{Family: "ipv4", Address: p.Gateway, OnLink: true}}}
	rules := []byte(`[{"priority":0,"src":"all","table":"local"},{"priority":32766,"src":"all","table":"main"},{"priority":32767,"src":"all","table":"default"}]`)
	routes := []byte(`[{"dst":"default","gateway":"192.0.2.1","dev":"eth0","flags":["onlink"]},{"dst":"192.0.2.53","dev":"eth0","scope":"link","flags":[]}]`)
	if e := ValidatePlanRouting(rules, routes, "-4", p, n); e != nil {
		t.Fatal(e)
	}
	if ValidatePlanRouting(rules, []byte(strings.Replace(string(routes), "192.0.2.53", "192.0.2.54", 1)), "-4", p, n) == nil {
		t.Fatal("undeclared route accepted")
	}
}
