package landing

import (
	"encoding/json"
	"strings"
	"testing"
)

func testServerPlan() ServerPlan {
	return ServerPlan{Revision: 1, Address: "100.64.0.8", Sources: []AuthorizedNode{{Address: "100.64.0.9", Credentials: Credentials{Username: "proxy-user-123456", Password: strings.Repeat("p", 32)}}}}
}

func TestNativeServerRequiresPrivateAuthenticatedBoundedSources(t *testing.T) {
	for _, scenario := range []string{"wildcard", "public", "loopback", "ipv6", "self", "no sources", "duplicate IP", "duplicate account", "empty password", "revision"} {
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
		case "no sources":
			plan.Sources = nil
		case "duplicate IP":
			plan.Sources = append(plan.Sources, plan.Sources[0])
			plan.Sources[1].Credentials.Username = "different-user-123456"
		case "duplicate account":
			plan.Sources = append(plan.Sources, plan.Sources[0])
			plan.Sources[1].Address = "100.64.0.10"
		case "empty password":
			plan.Sources[0].Credentials.Password = ""
		case "revision":
			plan.Revision = 0
		}
		if _, err := plan.RenderXray(); err == nil {
			t.Fatalf("accepted %s plan", scenario)
		}
	}
}

func TestNativeServerConfigurationHasNoImplicitDirectOrIPv6Fallback(t *testing.T) {
	plan := testServerPlan()
	raw, err := plan.RenderXray()
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	inbound := config["inbounds"].([]any)[0].(map[string]any)
	settings := inbound["settings"].(map[string]any)
	if inbound["listen"] != plan.Address || settings["ip"] != plan.Address || settings["auth"] != "password" || settings["udp"] != true || len(settings["accounts"].([]any)) != 1 || settings["users"] != nil {
		t.Fatal("private authenticated SOCKS and explicit UDP relay not configured")
	}
	outbounds := config["outbounds"].([]any)
	if outbounds[0].(map[string]any)["protocol"] != "blackhole" || outbounds[1].(map[string]any)["settings"].(map[string]any)["domainStrategy"] != "ForceIPv4" {
		t.Fatal("unexpected default or DNS fallback")
	}
	for _, token := range []string{"Requires=vastora-landing-firewall.service", "User=vastora-landing", "LoadCredential=", "MemoryMax=128M", "StandardOutput=null", "RestrictAddressFamilies=AF_INET AF_UNIX"} {
		if !strings.Contains(NativeUnit, token) {
			t.Fatalf("unit missing %s", token)
		}
	}
}
