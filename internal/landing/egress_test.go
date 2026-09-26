package landing

import (
	"net/netip"
	"strings"
	"testing"
)

func TestLandingSelectedEgress(t *testing.T) {
	for _, ip := range []string{"1.1.1.1", "10.0.0.2", "2001:4860:4860::8888"} {
		p := testServerPlan()
		p.EgressIP = ip
		for i := range p.Sources {
			p.Sources[i].TCPOnly = true
		}
		config, err := p.RenderDante(ip)
		if err != nil || !strings.Contains(string(config), "external: "+ip+"\n") {
			t.Fatalf("selected source was not bound: %s %v", ip, err)
		}
		if strings.Contains(ip, ":") && (!strings.Contains(string(config), "external.protocol: ipv6") || !strings.Contains(string(config), "to: ::/0\n command: connect")) {
			t.Fatal("IPv6 target family not selected")
		}
		if _, err := p.RenderDante("8.8.8.8"); err == nil {
			t.Fatal("silently accepted a different source")
		}
	}
	for _, ip := range []string{"::", "::1", "fe80::1", "fc00::1", "::ffff:1.1.1.1", "64:ff9b::a00:1", "2002:a00:1::1", "2001:db8::1", "2001::1", "3fff::1", "100.64.0.8", "169.254.169.254", "eth0\nclient pass {}"} {
		if ValidEgressIP(ip) {
			t.Fatalf("unsafe source %s", ip)
		}
	}
	p := testServerPlan()
	p.EgressIP = "2001:4860:4860::8888"
	if p.Validate() == nil {
		t.Fatal("accepted unsupported legacy UDP grants")
	}
}

func TestLandingIPv6ExitEvidence(t *testing.T) {
	const ip = "2001:4860:4860::8888"
	if !PublicIP(netip.MustParseAddr(ip)) {
		t.Fatal("public IPv6 rejected")
	}
	if got, err := traceExit([]byte("ip=" + ip + "\n")); err != nil || got != ip {
		t.Fatalf("IPv6 evidence: %s %v", got, err)
	}
	for _, ip := range []string{"::1", "fd00::1", "fe80::1", "64:ff9b::a00:1", "2002:a00:1::1", "2001:db8::1"} {
		if _, err := traceExit([]byte("ip=" + ip + "\n")); err == nil {
			t.Fatalf("unsafe exit: %s", ip)
		}
	}
}
