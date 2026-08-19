package deployer

import (
	"net"
	"strings"
	"testing"
)

func TestBundledServiceURLsRequireDNSAndPort8443(t *testing.T) {
	valid, err := normalizePublicURL(" https://Headscale.Example.com:8443/ ")
	if err != nil || valid != "https://headscale.example.com:8443" {
		t.Fatalf("valid URL normalized to %q: %v", valid, err)
	}
	for _, value := range []string{
		"http://headscale.example.com:8443",
		"https://headscale.example.com",
		"https://headscale.example.com:443",
		"https://127.0.0.1:8443",
		"https://user:secret@headscale.example.com:8443",
		"https://headscale.example.com:8443/path",
		"https://single-label:8443",
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

func TestGeneratedConfigurationKeeps443FreeAndSecretsOut(t *testing.T) {
	headscale := string(renderHeadscaleConfig("https://headscale.example.com:8443"))
	if !strings.Contains(headscale, "listen_addr: 127.0.0.1:8081") || strings.Contains(headscale, "tls_key_path") || strings.Contains(headscale, "extra_records:") {
		t.Fatalf("unexpected Headscale configuration:\n%s", headscale)
	}
	caddy := string(renderCaddyfile("https://center.example.com:8443", "127.0.0.1:8080", "https://headscale.example.com:8443"))
	if !strings.Contains(caddy, "https://center.example.com:8443") || !strings.Contains(caddy, "https://headscale.example.com:8443") || strings.Contains(caddy, ":443") {
		t.Fatalf("unexpected Caddy configuration:\n%s", caddy)
	}
}
