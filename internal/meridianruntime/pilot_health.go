package meridianruntime

import (
	"errors"
	"net/netip"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

// PilotPeerDeadlines never promotes process readiness to route readiness.
// Missing/stale health blocks routes, while a foreign identity is a source
// contract error. The expected peers and exits are pinned at takeover.
func PilotPeerDeadlines(checkpoint PilotCounters, receipt PilotReceipt, now time.Time) (map[string]int64, error) {
	deadlines := map[string]int64{}
	health := receipt.Health
	if health == nil {
		return deadlines, nil
	}
	if (health.Source == nil) != (checkpoint.Source == nil) ||
		health.Source != nil && *health.Source != *checkpoint.Source ||
		len(health.Peers) != len(checkpoint.Peers) {
		return nil, errors.New("meridianruntime: pilot route identities changed")
	}
	if health.SampledAt.IsZero() || health.SampledAt.After(now) || now.Sub(health.SampledAt) > landing.AllowLifetime {
		return deadlines, nil
	}
	for _, expected := range checkpoint.Peers {
		report, ok := health.Peers[expected.EgressID]
		if !ok || expected.Validate() != nil || report.Identity != expected.Identity ||
			report.Status.Revision != checkpoint.Revision {
			return nil, errors.New("meridianruntime: pilot route observation does not match pinned peer")
		}
		status := report.Status
		exit, err := netip.ParseAddr(status.ExitIP)
		if status.State == "healthy" && status.Stage == "complete" && status.Reason == "direct_and_business_ready" &&
			status.LinkState == "direct" && status.TCP && !status.UDP &&
			status.ExitIP == checkpoint.ExpectedExits[expected.EgressID] && err == nil && landing.PublicIP(exit) &&
			!status.StartedAt.IsZero() && !status.CheckedAt.Before(status.StartedAt) &&
			!status.CheckedAt.After(health.SampledAt) && now.Sub(status.CheckedAt) <= landing.AllowLifetime &&
			!status.LastHealthyAt.Before(status.StartedAt) && !status.LastHealthyAt.After(status.CheckedAt) &&
			status.AllowedUntil.After(now) && !status.AllowedUntil.After(status.StartedAt.Add(landing.AllowLifetime)) {
			deadlines[expected.EgressID] = status.AllowedUntil.UnixMilli()
		}
	}
	return deadlines, nil
}
