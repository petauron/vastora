package osreinstall

import (
	"strings"
	"testing"
)

func TestRoutingReviewRefusesUnrestorableRoutes(t *testing.T) {
	rules := "[{\"priority\":0,\"src\":\"all\",\"table\":\"local\"},{\"priority\":32766,\"src\":\"all\",\"table\":\"main\"},{\"priority\":32767,\"src\":\"all\",\"table\":\"default\"}]"
	routes := "[{\"dst\":\"default\",\"gateway\":\"192.0.2.1\",\"dev\":\"eth0\",\"flags\":[\"onlink\"]},{\"dst\":\"192.0.2.0/24\",\"dev\":\"eth0\",\"protocol\":\"kernel\",\"scope\":\"link\",\"prefsrc\":\"192.0.2.10\",\"flags\":[]},{\"type\":\"local\",\"dst\":\"127.0.0.0/8\",\"dev\":\"lo\",\"table\":\"local\",\"protocol\":\"kernel\",\"scope\":\"host\",\"prefsrc\":\"127.0.0.1\",\"flags\":[]},{\"type\":\"local\",\"dst\":\"192.0.2.10\",\"dev\":\"eth0\",\"table\":\"local\",\"protocol\":\"kernel\",\"scope\":\"host\",\"prefsrc\":\"192.0.2.10\",\"flags\":[]}]"
	network := staticNetworkEvidence()
	plan := StaticNetworkPlan{Interface: "eth0", MAC: network.MAC, Address: "192.0.2.10/24", Gateway: "192.0.2.1", GatewayOnLink: true, IPv6AcceptRA: "0"}
	if err := ValidatePlanRouting([]byte(rules), []byte(routes), "-4", plan, network); err != nil {
		t.Fatalf("normal kernel routes rejected: %v", err)
	}
	for name, input := range map[string]struct{ rules, routes string }{
		"policy rule": {strings.Replace(rules, "\"priority\":32766", "\"priority\":32766,\"fwmark\":\"0x1\"", 1), routes},
		"extra route": {rules, strings.TrimSuffix(routes, "]") + ",{\"dst\":\"198.51.100.0/24\",\"dev\":\"eth0\",\"gateway\":\"192.0.2.2\"}]"},
		"metric":      {rules, strings.Replace(routes, "\"gateway\":\"192.0.2.1\"", "\"gateway\":\"192.0.2.1\",\"metric\":10", 1)},
		"gateway":     {rules, strings.Replace(routes, "\"gateway\":\"192.0.2.1\"", "\"gateway\":\"192.0.2.2\"", 1)},
	} {
		t.Run(name, func(t *testing.T) {
			if ValidatePlanRouting([]byte(input.rules), []byte(input.routes), "-4", plan, network) == nil {
				t.Fatal("unrestorable route accepted")
			}
		})
	}
}

func TestRoutingReviewRefusesNonlocalIPv6(t *testing.T) {
	network := staticNetworkEvidence()
	plan := StaticNetworkPlan{Interface: "eth0", MAC: network.MAC, Address: "192.0.2.10/24", Gateway: "192.0.2.1", IPv6AcceptRA: "0"}
	rules := "[{\"priority\":0,\"src\":\"all\",\"table\":\"local\"},{\"priority\":32766,\"src\":\"all\",\"table\":\"main\"}]"
	routes := "[{\"dst\":\"fe80::/64\",\"dev\":\"eth0\",\"protocol\":\"kernel\",\"metric\":256,\"flags\":[],\"pref\":\"medium\"},{\"type\":\"local\",\"dst\":\"::1\",\"dev\":\"lo\",\"table\":\"local\",\"protocol\":\"kernel\",\"metric\":0,\"flags\":[],\"pref\":\"medium\"},{\"type\":\"local\",\"dst\":\"fe80::1234\",\"dev\":\"eth0\",\"table\":\"local\",\"protocol\":\"kernel\",\"metric\":0,\"flags\":[],\"pref\":\"medium\"},{\"type\":\"multicast\",\"dst\":\"ff00::/8\",\"dev\":\"eth0\",\"table\":\"local\",\"protocol\":\"kernel\",\"metric\":256,\"flags\":[],\"pref\":\"medium\"}]"
	if err := ValidatePlanRouting([]byte(rules), []byte(routes), "-6", plan, network); err != nil {
		t.Fatalf("kernel IPv6 routes rejected: %v", err)
	}
	extra := strings.TrimSuffix(routes, "]") + ",{\"dst\":\"default\",\"dev\":\"eth0\",\"gateway\":\"fe80::1\"}]"
	if ValidatePlanRouting([]byte(rules), []byte(extra), "-6", plan, network) == nil {
		t.Fatal("IPv6 default route accepted without a restoration plan")
	}
}
