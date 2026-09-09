package center

import (
	"context"
	"strings"
)

// Startup health alone is not a download-readiness signal: the co-located
// Agent can still be restoring Caddy/HAProxy after Center starts accepting
// requests. Wait for the host update and its actual ingress revisions first.
func (s *Server) agentUpdateRolloutReady(ctx context.Context) (bool, error) {
	if !s.startupReady.Load() || strings.TrimSpace(s.agentBinariesDir) == "" {
		return false, nil
	}
	if s.updates != nil {
		update, err := s.updates.CenterUpdateStatus(ctx)
		if err != nil {
			return false, err
		}
		if update.Available && update.State != "idle" && update.State != "succeeded" {
			return false, nil
		}
	}
	binding, configured, err := s.store.setupGatewayBinding(ctx)
	if err != nil {
		return false, err
	}
	if !configured {
		return true, nil
	}
	// Reuse the enrolled, fresh, unique owner of the configured host address.
	// A missing/ambiguous/offline owner is not evidence of a ready gateway.
	owner, err := s.store.coLocatedTailscaleEndpointOwner(ctx, binding.BindAddress)
	if err != nil || owner == nil {
		return false, err
	}
	var ready bool
	err = s.store.db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM gateway_components c JOIN gateway_states g ON g.gateway_node_id = c.gateway_node_id
		WHERE c.gateway_node_id = ? AND c.desired_status = 'running' AND c.status = 'ready'
		AND c.generation = c.applied_generation AND g.status = 'ready' AND g.desired_revision = g.applied_revision
		AND NOT EXISTS (SELECT 1 FROM node_listener_states n WHERE n.node_id = c.gateway_node_id
			AND (n.status NOT IN ('ready', 'stopped') OR n.desired_revision <> n.applied_revision))
	)`, owner.ID).Scan(&ready)
	return ready, err
}
