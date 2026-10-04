package agent

import (
	"context"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
)

// Network identity is observable before any application has been restored.
// This evidence does not advertise an installed landing client runtime.
func (s *Store) observePrivateNetworkPeer(ctx context.Context, candidates []networking.Candidate, ownership string) *landing.PeerIdentity {
	if ownership != "managed" {
		return nil
	}
	var identity *landing.PeerIdentity
	for _, candidate := range candidates {
		if candidate.Kind != networking.KindHeadscale || (landing.ServerPlan{Revision: 1, Address: candidate.Address}).Validate() != nil {
			continue
		}
		peer, err := s.linkChecker.SelfIdentity(ctx, candidate.Address)
		if err != nil {
			continue
		}
		if identity != nil && *identity != peer {
			return nil
		}
		identity = &peer
	}
	return identity
}
