//go:build linux && integration

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

// The generated firewall unit calls the current executable. The CI test binary
// supplies only that exact production subcommand, delegating to the real
// implementation rather than substituting a fake firewall service.
func TestMain(m *testing.M) {
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
