package agent

import (
	"context"
	"errors"
	"net/netip"
	"os/exec"
	"regexp"
	"strings"
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
		prefix := value.Subnet
		if !prefix.IsValid() || !prefix.Addr().Is4() {
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

func removeLandingSubscriptionAccess(ctx context.Context, address string) error {
	ip, err := netip.ParseAddr(address)
	if err != nil {
		return errors.New("agent: invalid stale subscription rule address")
	}
	if !netip.MustParsePrefix("100.64.0.0/10").Contains(ip) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	lines, err := landingSubscriptionInputRules(ctx)
	if err != nil {
		return errors.New("agent: cannot inspect stale subscription input rules")
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		for index := range fields {
			fields[index] = strings.Trim(fields[index], `"'`)
		}
		if !landingSubscriptionRuleMatches(fields, address) {
			continue
		}
		fields[0] = "-D"
		if err := runSubscriptionIPTables(ctx, fields...); err != nil {
			return errors.New("agent: stale subscription input rule cleanup requires explicit recovery")
		}
	}
	lines, err = landingSubscriptionInputRules(ctx)
	if err != nil {
		return errors.New("agent: cannot confirm stale subscription input rule cleanup")
	}
	for _, line := range lines {
		if landingSubscriptionRuleMatches(strings.Fields(line), address) {
			return errors.New("agent: stale subscription input rule cleanup was not confirmed")
		}
	}
	return nil
}

func landingSubscriptionInputRules(ctx context.Context) ([]string, error) {
	output, err := exec.CommandContext(ctx, "/usr/sbin/iptables", "-w", "5", "-S", "INPUT").Output()
	if err != nil || len(output) > 1<<20 {
		return nil, errors.New("agent: subscription input rules are unavailable")
	}
	return strings.Split(string(output), "\n"), nil
}

func landingSubscriptionRuleMatches(fields []string, address string) bool {
	if len(fields) < 3 || fields[0] != "-A" || fields[1] != "INPUT" {
		return false
	}
	required := map[string]string{"-d": address + "/32", "-p": "tcp", "--dport": "2097", "--comment": "vastora-subscription-origin", "-j": "ACCEPT"}
	seen := make(map[string]bool, len(required))
	bridge, subnet, commentMatch := "", "", false
	for index := 2; index+1 < len(fields); index++ {
		if fields[index] == "-i" {
			bridge = fields[index+1]
		}
		if fields[index] == "-s" {
			subnet = fields[index+1]
		}
		if fields[index] == "-m" && fields[index+1] == "comment" {
			commentMatch = true
		}
		value, exists := required[fields[index]]
		if !exists {
			continue
		}
		if seen[fields[index]] || strings.Trim(fields[index+1], `"`) != value {
			return false
		}
		seen[fields[index]] = true
		index++
	}
	prefix, err := netip.ParsePrefix(subnet)
	return len(seen) == len(required) && commentMatch && regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,14}$`).MatchString(bridge) && err == nil && prefix.Addr().Is4() && prefix.Addr().IsPrivate() && prefix.Bits() >= 8 && prefix == prefix.Masked()
}
