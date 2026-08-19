package deployer

import (
	"net"
	"strings"
	"testing"
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
	if !strings.Contains(headscale, "listen_addr: 127.0.0.1:8081") || strings.Contains(headscale, "tls_key_path") || strings.Contains(headscale, "extra_records:") {
		t.Fatalf("unexpected Headscale configuration:\n%s", headscale)
	}
	caddy := string(renderCaddyfile("https://center.example.com", "127.0.0.1:8080", "https://headscale.example.com"))
	if !strings.Contains(caddy, "https://center.example.com") || !strings.Contains(caddy, "https://headscale.example.com") || strings.Contains(caddy, ":8443") {
		t.Fatalf("unexpected Caddy configuration:\n%s", caddy)
	}
	policy := string(renderHeadscalePolicy())
	if strings.Contains(policy, "8443") || !strings.Contains(policy, `"ip": ["443"]`) {
		t.Fatalf("Headscale policy exposes the wrong Center ports:\n%s", policy)
	}
}
