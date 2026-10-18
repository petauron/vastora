//go:build linux && integration

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"golang.org/x/net/proxy"
)

// The generated firewall unit calls the current executable. The CI test binary
// supplies only that exact production subcommand, delegating to the real
// implementation rather than substituting a fake firewall service.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "agent" && os.Args[2] == "landing-recover-test" {
		if err := resumeNativeLanding(context.Background(), "landing-ci"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) == 3 && os.Args[1] == "agent" && os.Args[2] == "landing-firewall" {
		if err := RestoreLandingFirewall(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestLandingNativeHostLifecycle(t *testing.T) {
	if os.Getenv("VASTORA_NATIVE_LANDING_INTEGRATION") != "1" {
		t.Skip("dedicated disposable systemd CI runner only")
	}
	if os.Geteuid() != 0 {
		t.Fatal("root required")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Fatal("real systemd required", err)
	}
	for _, path := range []string{"/var/lib/vastora", "/etc/vastora-landing", landingUnitPath, landingFirewallUnitPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("refusing to touch an existing host resource", path, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := func(args ...string) {
		t.Helper()
		if output, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, output)
		}
	}
	command("ip", "link", "add", "vtland-ci", "type", "dummy")
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := RemoveLanding(cleanupCtx, "landing-ci", 3); err != nil {
			t.Error("remove owned CI landing service", err)
		}
		_ = exec.CommandContext(cleanupCtx, "ip", "link", "delete", "vtland-ci").Run()
	})
	command("ip", "address", "add", "100.64.0.8/32", "dev", "vtland-ci")
	command("ip", "link", "set", "vtland-ci", "up")
	plan := landing.ServerPlan{Revision: 1, Address: "100.64.0.8"}
	if err := ApplyLanding(ctx, "landing-ci", plan); err != nil {
		t.Fatal("install native landing service", err)
	}
	command("systemctl", "is-active", "--quiet", "vastora-landing.service")
	command("systemctl", "is-active", "--quiet", "vastora-landing-firewall.service")
	if err := ApplyLanding(ctx, "landing-ci", plan); err != nil {
		t.Fatal("idempotent installation", err)
	}
	testLandingBootRecovery(t, ctx, command)
	if err := RemoveLanding(ctx, "landing-ci", 2); err != nil {
		t.Fatal("disable native landing service", err)
	}
	for _, path := range landingPaths() {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("owned landing file remains", path, err)
		}
	}
	if err := ApplyLanding(ctx, "landing-ci", plan); err == nil {
		t.Fatal("stale install resurrected removed service")
	}
}

func testLandingBootRecovery(t *testing.T, ctx context.Context, command func(...string)) {
	t.Helper()
	const path = "/etc/legacy-landing-dns.nft"
	hook := []byte("[Service]\nExecStartPost=/usr/sbin/nft -f " + path + "\n")
	rules := []byte("insert rule inet vastora_landing_server_0123456789abcdef destinations ip daddr 100.100.100.100 meta l4proto { tcp, udp } th dport 53 return comment \"allow designated tailscale DNS\"\n")
	if err := os.MkdirAll(filepath.Dir(landingLegacyDNSHook), 0755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{landingLegacyDNSHook: hook, path: rules} {
		if err := os.WriteFile(name, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, name := range []string{landingLegacyDNSHook, landingLegacyDNSHook + ".vastora-retired", path} {
			if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Error(err)
			}
		}
	})
	command("systemctl", "stop", "vastora-landing.service", "vastora-landing-firewall.service")
	command("systemctl", "daemon-reload")
	if err := exec.CommandContext(ctx, "systemctl", "start", "vastora-landing.service").Run(); err == nil {
		t.Fatal("stale DNS table did not reproduce the failed prerequisite")
	}
	if err := resumeNativeLanding(ctx, "another-node"); err == nil {
		t.Fatal("recovery adopted another node's journal")
	}
	if _, err := os.Stat(landingLegacyDNSHook); err != nil {
		t.Fatal("foreign recovery changed the drop-in", err)
	}
	journal, err := readLandingHostJournal()
	if err != nil {
		t.Fatal(err)
	}
	journal.PendingFiles = map[string]string{landingConfigPath: journal.Files[landingConfigPath]}
	if err := saveLandingHostJournal(journal); err != nil {
		t.Fatal(err)
	}
	if err := resumeNativeLanding(ctx, "landing-ci"); err == nil {
		t.Fatal("pending transaction was resumed")
	}
	journal.PendingFiles = nil
	journal.Removed = true
	if err := saveLandingHostJournal(journal); err != nil {
		t.Fatal(err)
	}
	if err := resumeNativeLanding(ctx, "landing-ci"); err != nil {
		t.Fatal("removed service observation", err)
	}
	if err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "vastora-landing.service").Run(); err == nil {
		t.Fatal("removed service was resurrected")
	}
	journal.Removed = false
	if err := saveLandingHostJournal(journal); err != nil {
		t.Fatal(err)
	}
	configuration, err := os.ReadFile(landingConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(landingConfigPath, append(append([]byte{}, configuration...), []byte("# outside ownership\n")...), 0644); err != nil {
		t.Fatal(err)
	}
	if err := resumeNativeLanding(ctx, "landing-ci"); err == nil {
		t.Fatal("modified configuration was adopted")
	}
	if err := os.WriteFile(landingConfigPath, configuration, 0644); err != nil {
		t.Fatal(err)
	}
	if err := resumeNativeLanding(ctx, "landing-ci"); err != nil {
		t.Fatal("offline native service recovery", err)
	}
	if _, err := os.Lstat(landingLegacyDNSHook); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy hook still active", err)
	}
	if data, err := os.ReadFile(landingLegacyDNSHook + ".vastora-retired"); err != nil || string(data) != string(hook) {
		t.Fatal("original hook was not retained", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != string(rules) {
		t.Fatal("administrator nft file was modified", err)
	}
	command("systemctl", "is-active", "--quiet", "vastora-landing.service", "vastora-landing-firewall.service")
	if err := journal.Policy.Verify(ctx); err != nil {
		t.Fatal("recovered firewall was not exact", err)
	}
	if err := resumeNativeLanding(ctx, "landing-ci"); err != nil {
		t.Fatal("idempotent recovery", err)
	}
	command("systemctl", "disable", "--now", "vastora-landing.service")
	if err := resumeNativeLanding(ctx, "landing-ci"); err == nil {
		t.Fatal("administratively disabled service was resumed")
	}
	command("systemctl", "enable", "--now", "vastora-landing.service")
}

// Opt-in fixture for a disposable LXC only. The external harness owns CT
// reboot and cleanup; no production Center, keys or network identity is used.
func TestLandingNativeBootFixture(t *testing.T) {
	if os.Getenv("VASTORA_NATIVE_LANDING_BOOT_FIXTURE") != "1" {
		t.Skip("explicit disposable LXC boot fixture only")
	}
	if _, err := os.Stat("/run/systemd/container"); err != nil || os.Geteuid() != 0 {
		t.Fatal("disposable root systemd container required")
	}
	journal, err := readLandingHostJournal()
	if err != nil || journal != nil && (journal.NodeID != "landing-ci" || !journal.Removed || len(journal.Files) != 0 || journal.Policy != nil) {
		t.Fatal("refusing existing active or foreign landing ownership", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	plan := landing.ServerPlan{Revision: 1, Address: "100.64.0.8", Sources: []landing.AuthorizedNode{{Address: "100.64.0.9"}}}
	if journal != nil {
		plan.Revision = journal.Revision + 1
	}
	if err := ApplyLanding(ctx, "landing-ci", plan); err != nil {
		t.Fatal(err)
	}
}

func TestLandingNativeBootTrafficProbe(t *testing.T) {
	if os.Getenv("VASTORA_NATIVE_LANDING_BOOT_PROBE") != "1" {
		t.Skip("explicit disposable LXC client namespace only")
	}
	dialer, err := proxy.SOCKS5("tcp", "100.64.0.8:1080", nil, &net.Dialer{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: dialer.(proxy.ContextDialer).DialContext, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	// The harness assigns this well-known address to a disposable dummy link;
	// no packet leaves the LXC for this synthetic HTTP destination.
	response, err := client.Get("http://1.1.1.1:18080/")
	if err != nil {
		t.Fatal("post-boot SOCKS HTTP roundtrip", err)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 128))
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || string(data) != "landing-boot-ok\n" {
		t.Fatal("unexpected synthetic response", response.StatusCode, err)
	}
	response, err = client.Get("http://127.0.0.1:18080/")
	if err == nil {
		response.Body.Close()
		t.Fatal("private destination escaped the landing guard")
	}
	if !strings.Contains(err.Error(), "connection not allowed by ruleset") {
		t.Fatal("private destination failed without an explicit SOCKS policy denial", err)
	}
}
