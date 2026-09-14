package agent

import (
	"context"
	"errors"
	"net/netip"
	"os/exec"
	"regexp"
	"time"

	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
)

// Docker bridge traffic to an Agent listener traverses host INPUT, not Docker's
// published-port FORWARD rules. Do not expose this listener on a public address
// or admit any source outside the owned runtime bridge.
func openLandingSubscriptionAccess(ctx context.Context, installation AppliedInstallation) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	docker, bridge, _, err := openLandingDocker(ctx, installation.ApplicationID, "")
	if err != nil {
		return nil, err
	}
	defer docker.engine.Close()
	network, err := docker.engine.NetworkInspect(ctx, dockerruntime.NetworkName, client.NetworkInspectOptions{})
	if err != nil {
		return nil, errors.New("agent: subscription bridge is unavailable")
	}
	var subnet string
	for _, value := range network.Network.IPAM.Config {
		prefix, err := netip.ParsePrefix(value.Subnet)
		if err != nil || !prefix.Addr().Is4() {
			continue
		}
		if subnet != "" {
			return nil, errors.New("agent: ambiguous subscription bridge network")
		}
		subnet = prefix.String()
	}
	rule, err := landingSubscriptionInputRule(bridge, subnet, installation.ServiceAddress)
	if err != nil {
		return nil, err
	}
	err = runSubscriptionIPTables(ctx, append([]string{"-C", "INPUT"}, rule...)...)
	if err != nil {
		var status *exec.ExitError
		if !errors.As(err, &status) || status.ExitCode() != 1 {
			return nil, errors.New("agent: cannot inspect subscription input rule")
		}
		if err := runSubscriptionIPTables(ctx, append([]string{"-I", "INPUT", "1"}, rule...)...); err != nil {
			return nil, errors.New("agent: subscription input rule was not confirmed; explicit recovery required")
		}
	}
	if err := runSubscriptionIPTables(ctx, append([]string{"-C", "INPUT"}, rule...)...); err != nil {
		return nil, errors.New("agent: subscription input rule read-back failed")
	}
	return rule, nil
}

func landingSubscriptionInputRule(bridge, subnet, address string) ([]string, error) {
	prefix, err := netip.ParsePrefix(subnet)
	ip, ipErr := netip.ParseAddr(address)
	if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() || prefix.Bits() < 8 || prefix != prefix.Masked() || ipErr != nil || !netip.MustParsePrefix("100.64.0.0/10").Contains(ip) || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,14}$`).MatchString(bridge) {
		return nil, errors.New("agent: invalid private subscription input scope")
	}
	return []string{"-i", bridge, "-s", subnet, "-d", address + "/32", "-p", "tcp", "--dport", "2097", "-m", "comment", "--comment", "vastora-subscription-origin", "-j", "ACCEPT"}, nil
}

func runSubscriptionIPTables(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/sbin/iptables", append([]string{"-w", "5"}, args...)...).Run()
}
