package deployer

import (
	"net"
	"net/netip"
	"strings"
	"testing"

	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/petauron/vastora/internal/networking"
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

func TestPortPreflightReportsAConflict(t *testing.T) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := ensurePortsAvailable(port); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("occupied port was not rejected: %v", err)
	}
}

func TestGeneratedConfigurationUsesStandardHTTPSAndKeepsSecretsOut(t *testing.T) {
	headscale := string(renderHeadscaleConfig("https://headscale.example.com"))
	if !strings.Contains(headscale, "listen_addr: 0.0.0.0:8081") || strings.Contains(headscale, "tls_key_path") || strings.Contains(headscale, "extra_records:") {
		t.Fatalf("unexpected Headscale configuration:\n%s", headscale)
	}
	caddy := string(renderCaddyfile("https://center.example.com", "127.0.0.1:8080", "https://headscale.example.com", []string{"203.0.113.10"}))
	if !strings.Contains(caddy, "admin unix//run/vastora/caddy-admin.sock|0600") || !strings.Contains(caddy, "bind 127.0.0.1 203.0.113.10") || !strings.Contains(caddy, "tls /etc/caddy/system/center.crt /etc/caddy/system/center.key") || !strings.Contains(caddy, "handle /install/agent.sh") || !strings.Contains(caddy, "redir https://{host}{uri} 308") || !strings.Contains(caddy, "https://center.example.com") || !strings.Contains(caddy, "https://headscale.example.com") || strings.Contains(caddy, ":8443") {
		t.Fatalf("unexpected Caddy configuration:\n%s", caddy)
	}
	policy := string(renderHeadscalePolicy())
	if strings.Contains(policy, "8443") || !strings.Contains(policy, `"ip": ["443"]`) {
		t.Fatalf("Headscale policy exposes the wrong Center ports:\n%s", policy)
	}
}

func TestGatewayBindsOnlyResolvedLocalPublicAddresses(t *testing.T) {
	resolutions := []gatewayResolution{
		{hostname: "center.example.com", addresses: []netip.Addr{netip.MustParseAddr("203.0.113.20"), netip.MustParseAddr("198.51.100.5")}},
		{hostname: "headscale.example.com", addresses: []netip.Addr{netip.MustParseAddr("203.0.113.20"), netip.MustParseAddr("2001:db8::20")}},
	}
	candidates := []networking.Candidate{
		{Address: "203.0.113.20", Kind: networking.KindPublic},
		{Address: "2001:db8::20", Kind: networking.KindPublic},
		{Address: "100.64.0.1", Kind: networking.KindHeadscale},
		{Address: "192.168.1.10", Kind: networking.KindLAN},
	}
	addresses, err := selectGatewayBindAddresses(resolutions, candidates)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"203.0.113.20", "[2001:db8::20]"}
	if strings.Join(addresses, ",") != strings.Join(want, ",") {
		t.Fatalf("gateway bind addresses = %#v, want %#v", addresses, want)
	}
	if _, err := selectGatewayBindAddresses([]gatewayResolution{{hostname: "wrong.example.com", addresses: []netip.Addr{netip.MustParseAddr("198.51.100.5")}}}, candidates); err == nil || !strings.Contains(err.Error(), "must resolve directly") {
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
	if string(hostConfig.NetworkMode) != "bridge" {
		t.Fatalf("Headscale network mode = %q, want bridge", hostConfig.NetworkMode)
	}
	port := dockernetwork.MustParsePort("8081/tcp")
	bindings := hostConfig.PortBindings[port]
	if len(bindings) != 1 || bindings[0].HostIP != netip.MustParseAddr("127.0.0.1") || bindings[0].HostPort != "8081" {
		t.Fatalf("Headscale HTTP bindings = %#v", bindings)
	}
	if _, exposed := config.ExposedPorts[port]; !exposed {
		t.Fatalf("Headscale HTTP port is not exposed: %#v", config.ExposedPorts)
	}
}
