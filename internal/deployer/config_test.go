package deployer

import (
	"net/netip"
	"strings"
	"testing"

	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/networking"
	"gopkg.in/yaml.v3"
)

func TestBundledServiceURLsUseStandardHTTPS(t *testing.T) {
	valid, err := normalizePublicURL(" https://Headscale.Example.com:443/ ")
	if err != nil || valid != "https://headscale.example.com" {
		t.Fatalf("valid URL normalized to %q: %v", valid, err)
	}
	if valid, err := normalizePublicURL("https://headscale.example.com"); err != nil || valid != "https://headscale.example.com" {
		t.Fatalf("implicit standard HTTPS URL normalized to %q: %v", valid, err)
	}
	for _, value := range []string{
		"http://headscale.example.com",
		"https://headscale.example.com:8443",
		"https://127.0.0.1:8443",
		"https://user:secret@headscale.example.com",
		"https://headscale.example.com/path",
		"https://single-label",
	} {
		if _, err := normalizePublicURL(value); err == nil {
			t.Fatalf("unsafe URL was accepted: %s", value)
		}
	}
}

func TestGeneratedConfigurationUsesStandardHTTPSAndKeepsSecretsOut(t *testing.T) {
	headscalePayload := renderHeadscaleConfig("https://headscale.example.com")
	var parsed struct {
		DERP struct {
			URLs  []string `yaml:"urls"`
			Paths []string `yaml:"paths"`
		} `yaml:"derp"`
	}
	if err := yaml.Unmarshal(headscalePayload, &parsed); err != nil {
		t.Fatalf("generated Headscale configuration is invalid YAML: %v\n%s", err, headscalePayload)
	}
	if len(parsed.DERP.URLs) != 0 || len(parsed.DERP.Paths) != 1 || parsed.DERP.Paths[0] != "/etc/headscale/derp.yaml" {
		t.Fatalf("generated Headscale DERP sources = %#v", parsed.DERP)
	}
	headscale := string(headscalePayload)
	for _, expected := range []string{
		"listen_addr: 0.0.0.0:8081",
		"override_local_dns: true",
		"      - 1.1.1.1",
		"    enabled: true",
		"    verify_clients: true",
		"    automatically_add_embedded_derp_region: true",
		"  urls: []",
		"    - /etc/headscale/derp.yaml",
		"  auto_update_enabled: false",
		"disable_check_updates: true",
		"logtail:\n  enabled: false",
		"auto_update:\n  enabled: false",
		"prefixes:\n  v4: 100.64.0.0/10",
	} {
		if !strings.Contains(headscale, expected) {
			t.Fatalf("Headscale configuration is missing %q:\n%s", expected, headscale)
		}
	}
	derpMapPayload := renderHeadscaleDERPMap()
	var parsedDERPMap map[string]any
	if err := yaml.Unmarshal(derpMapPayload, &parsedDERPMap); err != nil {
		t.Fatalf("generated DERP map is invalid YAML: %v\n%s", err, derpMapPayload)
	}
	derpMap := string(derpMapPayload)
	for _, expected := range []string{"hostname: stun.cloudflare.com", "stunport: 3478", "stunonly: true", "derpport: 0"} {
		if !strings.Contains(derpMap, expected) {
			t.Fatalf("Cloudflare STUN map is missing %q:\n%s", expected, derpMap)
		}
	}
	if strings.Contains(derpMap, "turn.cloudflare.com") || strings.Contains(derpMap, "stunonly: false") {
		t.Fatalf("Cloudflare was configured as a relay:\n%s", derpMap)
	}
	if strings.Contains(headscale, "controlplane.tailscale.com") || strings.Contains(headscale, "tls_key_path") || strings.Contains(headscale, "extra_records:") || strings.Contains(headscale, "v6:") {
		t.Fatalf("unexpected Headscale configuration:\n%s", headscale)
	}
	caddy := string(renderCaddyfile("https://center.example.com", dockerruntime.CenterAlias+":8080", "https://headscale.example.com", []string{"100.64.0.1"}, []string{"203.0.113.10"}, []deployapi.CenterEndpointAlias{{URL: "https://old-center.example.com"}}, nil))
	if !strings.Contains(caddy, "admin unix//run/vastora/caddy-admin.sock|0600") || strings.Count(caddy, "bind 0.0.0.0") < 6 || !strings.Contains(caddy, "reverse_proxy "+dockerruntime.CenterAlias+":8080") || !strings.Contains(caddy, "reverse_proxy "+dockerruntime.HeadscaleAlias+":8081") || !strings.Contains(caddy, "tls /etc/caddy/system/center.crt /etc/caddy/system/center.key") || !strings.Contains(caddy, "handle /install/agent.sh") || !strings.Contains(caddy, "handle /api/v1/agent-binaries/*") || !strings.Contains(caddy, "redir https://{host}{uri} 308") || !strings.Contains(caddy, "https://center.example.com:12443") || !strings.Contains(caddy, "https://center.example.com:10443") || !strings.Contains(caddy, "https://headscale.example.com:443") || strings.Contains(caddy, ":8443") {
		t.Fatalf("unexpected Caddy configuration:\n%s", caddy)
	}
	policy := string(renderHeadscalePolicy())
	if strings.Contains(policy, "8443") || !strings.Contains(policy, `"ip": ["443"]`) {
		t.Fatalf("Headscale policy exposes the wrong Center ports:\n%s", policy)
	}
}

func TestGatewayBindsConfirmedNATAddressOnlyWhenDNSMatches(t *testing.T) {
	resolutions := []gatewayResolution{
		{hostname: "center.example.com", addresses: []netip.Addr{netip.MustParseAddr("203.0.113.20")}},
		{hostname: "headscale.example.com", addresses: []netip.Addr{netip.MustParseAddr("203.0.113.20")}},
	}
	candidates := []networking.Candidate{{Address: "10.0.0.157", Kind: networking.KindLAN}}
	addresses, err := selectMappedGatewayBindAddress(resolutions, candidates, "203.0.113.20", "10.0.0.157")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.157"}
	if strings.Join(addresses, ",") != strings.Join(want, ",") {
		t.Fatalf("gateway bind addresses = %#v, want %#v", addresses, want)
	}
	if _, err := selectMappedGatewayBindAddress([]gatewayResolution{{hostname: "wrong.example.com", addresses: []netip.Addr{netip.MustParseAddr("198.51.100.5")}}}, candidates, "203.0.113.20", "10.0.0.157"); err == nil || !strings.Contains(err.Error(), "does not resolve") {
		t.Fatalf("non-local DNS address was accepted: %v", err)
	}
}

func TestHeadscaleContainerIsolatedFromHostTailscale(t *testing.T) {
	installer := DockerHeadscaleInstaller{
		HeadscaleImage:        DefaultHeadscaleImage,
		HeadscaleDataVolume:   "headscale-data",
		HeadscaleConfigVolume: "headscale-config",
		CenterDataVolume:      "center-data",
	}
	config, hostConfig := installer.headscaleContainerConfig()
	if string(hostConfig.NetworkMode) != dockerruntime.NetworkName {
		t.Fatalf("Headscale network mode = %q, want %s", hostConfig.NetworkMode, dockerruntime.NetworkName)
	}
	port := dockernetwork.MustParsePort("8081/tcp")
	bindings := hostConfig.PortBindings[port]
	if len(bindings) != 0 {
		t.Fatalf("Headscale HTTP must stay private to the runtime network: %#v", bindings)
	}
	if _, exposed := config.ExposedPorts[port]; !exposed {
		t.Fatalf("Headscale HTTP port is not exposed: %#v", config.ExposedPorts)
	}
	stunPort := dockernetwork.MustParsePort("3478/udp")
	stunBindings := hostConfig.PortBindings[stunPort]
	if len(stunBindings) != 1 || stunBindings[0].HostIP != netip.MustParseAddr("0.0.0.0") || stunBindings[0].HostPort != "3478" {
		t.Fatalf("Headscale STUN bindings = %#v", stunBindings)
	}
	if _, exposed := config.ExposedPorts[stunPort]; !exposed {
		t.Fatalf("Headscale STUN port is not exposed: %#v", config.ExposedPorts)
	}
}
