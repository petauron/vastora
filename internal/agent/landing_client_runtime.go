package agent

import (
	"context"

	"github.com/petauron/vastora/internal/landing"
)

func (s *Store) observeLandingClientRuntime(ctx context.Context) *landing.ClientRuntime {
	installation, err := s.AppliedInstallation(ctx, threeXUIKey)
	if err != nil {
		return nil
	}
	s.landingSubscriptionMu.RLock()
	ready := s.landingSubscriptionAddress == installation.ServiceAddress
	s.landingSubscriptionMu.RUnlock()
	if !ready {
		return nil
	}
	peer, err := landing.NewLinkChecker().SelfIdentity(ctx, installation.ServiceAddress)
	if err != nil {
		return nil
	}
	return &landing.ClientRuntime{Generation: landing.ClientRuntimeGeneration, Peer: peer}
}
