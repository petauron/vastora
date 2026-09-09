package agent

import (
	"context"
	"slices"
	"sync"

	"github.com/petauron/vastora/internal/landing"
)

func (s *Store) readLandingLatencies() []landing.LatencyObservation {
	s.landingLatencyMu.Lock()
	defer s.landingLatencyMu.Unlock()
	if s.landingLatencies == nil {
		return []landing.LatencyObservation{}
	}
	return slices.Clone(s.landingLatencies)
}

// A bounded read-only batch. It never installs services, changes routes or
// creates a firewall lease, including when no landing route is enabled.
func (s *Store) observeLandingLatencies(ctx context.Context, targets []landing.LatencyTarget) {
	if len(targets) > landing.MaxServers {
		targets = nil
	}
	observations := make([]landing.LatencyObservation, len(targets))
	var workers sync.WaitGroup
	slots := make(chan struct{}, 4)
	for index, target := range targets {
		slots <- struct{}{}
		workers.Go(func() {
			defer func() { <-slots }()
			checker := landing.NewLinkChecker()
			defer checker.HTTPClient.CloseIdleConnections()
			result := checker.Check(ctx, target.Peer)
			observations[index] = landing.LatencyObservation{Target: target, State: result.State, LatencyMS: result.LatencyMS, CheckedAt: result.CheckedAt}
		})
	}
	workers.Wait()
	s.landingLatencyMu.Lock()
	s.landingLatencies = observations
	s.landingLatencyMu.Unlock()
}
