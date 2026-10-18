package agent

import (
	"context"
	"errors"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

const landingLegacyDNSHook = landingFirewallUnitPath + ".d/90-local-dns.conf"

var landingLegacyDNSPath = regexp.MustCompile(`^/etc/[A-Za-z0-9][A-Za-z0-9_-]*\.nft$`)
var landingLegacyDNSRule = regexp.MustCompile(`^insert rule inet (vastora_landing_server_[0-9a-f]{16}) destinations (oifname "tailscale0" )?(ip daddr 100\.100\.100\.100|ip6 daddr fd7a:115c:a1e0::53) meta l4proto \{ tcp, udp \} th dport 53 (counter )?return comment "(allow designated tailscale DNS|designated tailnet DNS)"$`)

// Only the obsolete, exact-purpose tailnet DNS workaround is retired. Arbitrary
// administrator drop-ins and nft files are neither executed nor modified here.
func legacyLandingDNSPath(hook, rules []byte) string {
	lines := strings.Split(strings.TrimSpace(string(hook)), "\n")
	if len(lines) != 2 || lines[0] != "[Service]" {
		return ""
	}
	path, ok := strings.CutPrefix(lines[1], "ExecStartPost=/usr/sbin/nft -f ")
	if !ok || !landingLegacyDNSPath.MatchString(path) {
		return ""
	}
	entries := strings.Split(strings.TrimSpace(string(rules)), "\n")
	if len(entries) < 1 || len(entries) > 2 {
		return ""
	}
	table := ""
	seen := map[string]bool{}
	for _, line := range entries {
		match := landingLegacyDNSRule.FindStringSubmatch(line)
		if match == nil || table != "" && table != match[1] || seen[match[3]] {
			return ""
		}
		table, seen[match[3]] = match[1], true
	}
	return path
}

func retireLegacyLandingDNSHook() error {
	hook, err := readLandingOwnedFile(landingLegacyDNSHook)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(hook)), "\n")
	if len(lines) != 2 {
		return nil
	}
	path, ok := strings.CutPrefix(lines[1], "ExecStartPost=/usr/sbin/nft -f ")
	if !ok || !landingLegacyDNSPath.MatchString(path) {
		return nil
	}
	rules, err := readLandingOwnedFile(path)
	if err != nil {
		return err
	}
	if legacyLandingDNSPath(hook, rules) == "" {
		return nil
	}
	resolver, err := readLandingOwnedFile("/etc/vastora-landing/resolv.conf")
	if err != nil || string(resolver) != landing.NativeResolver {
		return errors.New("agent: legacy DNS hook requires the managed landing resolver")
	}
	// Rename keeps the original bytes as a root-owned recovery copy. systemd
	// reads only *.conf; never overwrite an existing recovery copy.
	backup := landingLegacyDNSHook + ".vastora-retired"
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		return errors.New("agent: landing DNS recovery copy already exists or is unavailable")
	}
	return os.Rename(landingLegacyDNSHook, backup)
}

// ResumeNativeLanding restores only a fully committed, owned native service.
// It deliberately runs before the first heartbeat: Center availability is not
// a prerequisite for reboot recovery. Pending changes and tombstones stay shut.
func (s *Store) ResumeNativeLanding(ctx context.Context) error {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return nil
	}
	s.landingMutationMu.Lock()
	defer s.landingMutationMu.Unlock()
	connection, err := s.Connection(ctx)
	if err != nil {
		return err
	}
	return resumeNativeLanding(ctx, connection.AgentID)
}

func resumeNativeLanding(ctx context.Context, nodeID string) error {
	journal, err := readLandingHostJournal()
	if err != nil || journal == nil || journal.Removed {
		return err
	}
	if journal.NodeID != nodeID || journal.Policy == nil || journal.Policy.Revision != journal.Revision || journal.PreviousPolicy != nil || len(journal.PendingFiles) != 0 {
		return errors.New("agent: landing boot recovery requires a committed owned configuration")
	}
	if err := verifyLandingFiles(journal); err != nil {
		return err
	}
	for _, path := range landingPaths() {
		if _, err := readLandingOwnedFile(path); err != nil {
			return errors.New("agent: landing boot configuration is incomplete")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Respect explicit disablement. Recovery must not resurrect a removed or
	// administratively disabled instance simply because an old journal exists.
	if _, err := landingCommand(ctx, "systemctl", "is-enabled", "--quiet", "vastora-landing.service"); err != nil {
		return errors.New("agent: landing boot recovery requires an enabled service")
	}
	if err := retireLegacyLandingDNSHook(); err != nil {
		return err
	}
	if _, err := landingCommand(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	// Validate and install the saved guard, including the service UID, before
	// starting anything. Do not replace unknown policy or bypass a failed guard.
	if err := RestoreLandingFirewall(ctx); err != nil {
		return err
	}
	if _, err := landingCommand(ctx, "systemctl", "reset-failed", "vastora-landing-firewall.service", "vastora-landing.service"); err != nil {
		return err
	}
	if _, err := landingCommand(ctx, "systemctl", "start", "vastora-landing-firewall.service", "vastora-landing.service"); err != nil {
		return err
	}
	return waitLandingListener(ctx, journal.Policy.Address, journal.UID)
}
