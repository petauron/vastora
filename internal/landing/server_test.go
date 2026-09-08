package landing

import (
	"strings"
	"testing"
)

func testServerPlan() ServerPlan {
	return ServerPlan{Revision: 1, Address: "100.64.0.8", Sources: []AuthorizedNode{{Address: "100.64.0.9"}}}
}
func TestNativeServerRequiresPrivateBoundedSources(t *testing.T) {
	for _, scenario := range []string{"wildcard", "public", "loopback", "ipv6", "self", "duplicate IP", "revision"} {
		plan := testServerPlan()
		switch scenario {
		case "wildcard":
			plan.Address = "0.0.0.0"
		case "public":
			plan.Address = "1.1.1.1"
		case "loopback":
			plan.Address = "127.0.0.1"
		case "ipv6":
			plan.Address = "fd7a:115c:a1e0::1"
		case "self":
			plan.Sources[0].Address = plan.Address
		case "duplicate IP":
			plan.Sources = append(plan.Sources, plan.Sources[0])
		case "revision":
			plan.Revision = 0
		}
		if _, err := plan.RenderDante("10.0.0.2"); err == nil {
			t.Fatalf("accepted %s", scenario)
		}
	}
}
func TestNativeDantePrivateMembershipAndUDP(t *testing.T) {
	raw, err := testServerPlan().RenderDante("10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	config := string(raw)
	for _, side := range []string{"internal", "external"} {
		if strings.Index(config, side+".protocol:") > strings.Index(config, side+":") {
			t.Fatalf("%s protocol must precede its address", side)
		}
	}
	for _, token := range []string{"internal: 100.64.0.8 port = 1080", "external: 10.0.0.2", "clientmethod: none", "socksmethod: none", "from: 100.64.0.9/32 to: 100.64.0.8/32", "command: connect udpassociate", "udp.portrange: 1081-1208", "internal.protocol: ipv4", "external.protocol: ipv4"} {
		if !strings.Contains(config, token) {
			t.Fatalf("missing %s", token)
		}
	}
	if strings.Contains(config, "password") || strings.Contains(config, "command: bind") {
		t.Fatal("unexpected password or BIND")
	}
	plan := testServerPlan()
	plan.Sources = nil
	raw, err = plan.RenderDante("10.0.0.2")
	if err != nil || strings.Contains(string(raw), "client pass") || strings.Contains(string(raw), "socks pass") {
		t.Fatal("unselected server must deny all")
	}
	for _, token := range []string{"User=vastora-landing", "ExecStart=/etc/vastora-landing/danted", "Requires=vastora-landing-firewall.service", "MemoryMax=128M", "StandardOutput=null", "BindReadOnlyPaths=/etc/vastora-landing/resolv.conf", "BindReadOnlyPaths=/etc/vastora-landing/nsswitch.conf"} {
		if !strings.Contains(NativeUnit, token) {
			t.Fatalf("unit missing %s", token)
		}
	}
}
func TestDanteEgressCannotUseWildcardOrPrivateMesh(t *testing.T) {
	for _, value := range []string{"0.0.0.0", "127.0.0.1", "169.254.169.254", "100.64.0.8", "::1", "2001:4860:4860::8888", "eth0\nclient pass {}"} {
		if _, err := testServerPlan().RenderDante(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
func TestUDPRelayRangeBoundToExactPeer(t *testing.T) {
	for _, value := range []string{"100.64.0.8:1081", "100.64.0.8:1208"} {
		if !validUDPRelay(value, "100.64.0.8") {
			t.Fatal(value)
		}
	}
	for _, value := range []string{"100.64.0.8:1080", "100.64.0.8:1209", "100.64.0.9:1081", "[::1]:1081"} {
		if validUDPRelay(value, "100.64.0.8") {
			t.Fatal(value)
		}
	}
}
