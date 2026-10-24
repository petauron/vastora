package meridianruntime

import (
	"errors"
	"net/netip"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

// PilotPeerReadiness never promotes process readiness to route readiness.
// Missing startup acceptance blocks routes; a foreign identity is a source
// contract error. The expected peers and exits are pinned at takeover.
func PilotPeerReadiness(checkpoint PilotCounters, receipt PilotReceipt, now time.Time) (map[string]bool, error) {
	readiness := map[string]bool{}
	health := receipt.Health
	revision := checkpoint.Revision
	if receipt.RuntimeRevision != 0 {
		if receipt.RuntimeRevision < revision {
			return nil, errors.New("meridianruntime: runtime revision regressed")
		}
		revision = receipt.RuntimeRevision
	}
	if health == nil {
		return readiness, nil
	}
	if (health.Source == nil) != (checkpoint.Source == nil) ||
		health.Source != nil && *health.Source != *checkpoint.Source ||
		len(health.Peers) != len(checkpoint.Peers) {
		return nil, errors.New("meridianruntime: pilot route identities changed")
	}
	if health.SampledAt.IsZero() || health.SampledAt.After(now) {
		return readiness, nil
	}
	for _, expected := range checkpoint.Peers {
		report, ok := health.Peers[expected.EgressID]
		if !ok || expected.Validate() != nil || report.Identity != expected.Identity ||
			report.Status.Revision != revision {
			return nil, errors.New("meridianruntime: pilot route observation does not match pinned peer")
		}
		status := report.Status
		exit, err := netip.ParseAddr(status.ExitIP)
		if status.State == "healthy" && status.Stage == "startup" && status.Reason == "startup_ready" &&
			(status.LinkState == "direct" || status.LinkState == "authenticated") && status.TCP && !status.UDP &&
			status.ExitIP == checkpoint.ExpectedExits[expected.EgressID] && err == nil && landing.PublicIP(exit) &&
			!status.StartedAt.IsZero() && !status.CheckedAt.Before(status.StartedAt) &&
			!status.CheckedAt.After(health.SampledAt) && status.AllowedUntil.IsZero() {
			readiness[expected.EgressID] = true
		}
	}
	return readiness, nil
}
