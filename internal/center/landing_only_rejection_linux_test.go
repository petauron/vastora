package center

import (
	"context"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

// Run while the private fixture waits for fault injection, with its source
// authorized. Require an explicit SOCKS ruleset denial, not just an HTTP/TLS
// error or unreachable target which could otherwise yield a false positive.
func TestLandingOnlyPrivateRejectsLocalTargets(t *testing.T) {
	if os.Getenv("VASTORA_DISPOSABLE_PRIVATE_LAB") != "issue-940" {
		t.Skip("disposable private-network lab only")
	}
	host, _ := os.Hostname()
	if runtime.GOOS != "linux" || host != "vastora-940-control-test" {
		t.Fatal("unexpected lab host")
	}
	b, err := os.ReadFile("/opt/vastora940/landing-address")
	if err != nil {
		t.Fatal(err)
	}
	control, err := net.LookupIP("center.vastora940.test")
	if err != nil || len(control) == 0 {
		t.Fatal("lab control address unavailable")
	}
	d, err := proxy.SOCKS5("tcp", net.JoinHostPort(strings.TrimSpace(string(b)), "1080"), nil, &net.Dialer{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"127.0.0.1:80", "169.254.169.254:80", "1.1.1.1:22", net.JoinHostPort(control[0].String(), "443")} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, e := d.(proxy.ContextDialer).DialContext(ctx, "tcp", target)
		cancel()
		if c != nil {
			c.Close()
		}
		if e == nil || !strings.Contains(e.Error(), "connection not allowed by ruleset") {
			t.Fatalf("expected explicit ruleset denial, got %v", e)
		}
	}
}
