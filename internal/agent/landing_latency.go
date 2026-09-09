package agent

import (
	"context"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

func (s *Store) setLandingLatencyTargets(targets []landing.LatencyTarget) {
	if len(targets) > landing.MaxServers {
		targets = nil
	}
	s.landingLatencyMu.Lock()
	defer s.landingLatencyMu.Unlock()
	if slices.Equal(s.landingLatencyTargets, targets) {
		return
	}
	s.landingLatencyTargets = slices.Clone(targets)
	if s.landingLatencyChanged != nil {
		close(s.landingLatencyChanged)
	}
	s.landingLatencyChanged = make(chan struct{})
}

func (s *Store) landingLatencyTargetSnapshot() ([]landing.LatencyTarget, <-chan struct{}) {
	s.landingLatencyMu.Lock()
	defer s.landingLatencyMu.Unlock()
	if s.landingLatencyChanged == nil {
		s.landingLatencyChanged = make(chan struct{})
	}
	return slices.Clone(s.landingLatencyTargets), s.landingLatencyChanged
}

// Each target has its own clock and reports immediately. No probe or report
// blocks the management heartbeat, and only four probes run at once.
func (c Client) RunLandingLatencyChecks(ctx context.Context, store *Store, report func(error)) {
	checker := landing.NewLinkChecker()
	defer checker.HTTPClient.CloseIdleConnections()
	slots := make(chan struct{}, 4)
	store.runLandingLatencyChecks(ctx, 15*time.Second, func(ctx context.Context, target landing.LatencyTarget) {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		result := checker.Check(ctx, target.Peer)
		<-slots
		if ctx.Err() != nil {
			return
		}
		observation := landing.LatencyObservation{Target: target, State: result.State, LatencyMS: result.LatencyMS, CheckedAt: result.CheckedAt}
		requestContext, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		connection, err := store.Connection(requestContext)
		if err == nil {
			endpoint := connection.CenterURL + "/api/v1/agents/" + url.PathEscape(connection.AgentID) + "/landing-latencies"
			err = c.post(requestContext, endpoint, observation, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, nil)
		}
		if err != nil && ctx.Err() == nil && report != nil {
			report(err)
		}
	})
}

func (s *Store) runLandingLatencyChecks(ctx context.Context, interval time.Duration, probe func(context.Context, landing.LatencyTarget)) {
	type worker struct {
		target landing.LatencyTarget
		cancel context.CancelFunc
	}
	active := map[string]worker{}
	var workers sync.WaitGroup
	defer func() {
		for _, current := range active {
			current.cancel()
		}
		workers.Wait()
	}()
	for {
		targets, changed := s.landingLatencyTargetSnapshot()
		for id, current := range active {
			if !slices.Contains(targets, current.target) {
				current.cancel()
				delete(active, id)
			}
		}
		for _, target := range targets {
			if _, exists := active[target.NodeID]; exists {
				continue
			}
			workerContext, cancel := context.WithCancel(ctx)
			active[target.NodeID] = worker{target, cancel}
			workers.Go(func() {
				for workerContext.Err() == nil {
					probe(workerContext, target)
					timer := time.NewTimer(interval)
					select {
					case <-workerContext.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		}
	}
}
