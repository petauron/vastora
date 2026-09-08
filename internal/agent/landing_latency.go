package agent

import (
	"context"

	"github.com/petauron/vastora/internal/landing"
)

func (s *Store) readLandingLatency() *landing.LatencyObservation {
	s.landingLatencyMu.Lock()
	defer s.landingLatencyMu.Unlock()
	return s.landingLatency
}

// One bounded measurement per heartbeat, including when landing is disabled.
// This does not install Dante, change routes, or open a business firewall lease.
func (s *Store) observeLandingLatency(ctx context.Context, target *landing.LatencyTarget) {
	var observation *landing.LatencyObservation
	if target != nil {
		checker := landing.NewLinkChecker()
		defer checker.HTTPClient.CloseIdleConnections()
		result := checker.Check(ctx, target.Peer)
		observation = &landing.LatencyObservation{Target: *target, State: result.State, LatencyMS: result.LatencyMS, CheckedAt: result.CheckedAt}
	}
	s.landingLatencyMu.Lock()
	s.landingLatency = observation
	s.landingLatencyMu.Unlock()
}
