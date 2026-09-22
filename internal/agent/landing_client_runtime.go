package agent

import (
	"context"
	"errors"

	"github.com/petauron/vastora/internal/landing"
)

func (s *Store) observeLandingClientRuntime(ctx context.Context) *landing.ClientRuntime {
	installation, err := s.AppliedInstallation(ctx, meridianKey)
	if errors.Is(err, errApplicationNotInstalled) {
		installation, err = s.AppliedInstallation(ctx, threeXUIKey)
	}
	if err != nil {
		return nil
	}
	if installation.AppKey == meridianKey {
		// Meridian has no controller/worker topology. Every installed runtime can
		// expose its authenticated private landing peer capability.
	} else if installation.ApplicationRole == "master" {
		s.landingSubscriptionMu.RLock()
		ready := s.landingSubscriptionAddress == installation.ServiceAddress
		s.landingSubscriptionMu.RUnlock()
		if !ready {
			return nil
		}
	} else if installation.ApplicationRole != "worker" {
		return nil
	}
	peer, err := s.linkChecker.SelfIdentity(ctx, installation.ServiceAddress)
	if err != nil {
		return nil
	}
	return &landing.ClientRuntime{Generation: landing.ClientRuntimeGeneration, Peer: peer}
}
