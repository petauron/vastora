package center

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/petauron/vastora/internal/deployapi"

	"github.com/petauron/vastora/internal/landing"
)

// Keep a source authorized until explicit proxy-disable completion removes it
// from the server plan. Queued enables need the grant before their first probe.
func (s *Store) landingAccessRules(ctx context.Context) ([]landing.AccessRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT node_id,desired_json FROM landing_server_states ORDER BY node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := []landing.AccessRule{}
	for rows.Next() {
		var nodeID string
		var encoded []byte
		if err := rows.Scan(&nodeID, &encoded); err != nil {
			return nil, err
		}
		var state landing.ServerState
		if json.Unmarshal(encoded, &state) != nil || state.Validate() != nil || state.NodeID != nodeID {
			return nil, errors.New("center: invalid landing access state")
		}
		if state.Plan == nil {
			continue
		}
		for _, source := range state.Plan.Sources {
			rules = append(rules, landing.AccessRule{Source: source.Address, Destination: state.Plan.Address})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return landing.NormalizeAccessRules(rules)
}

func (s *Server) syncLandingAccess(ctx context.Context) error {
	manager, ok := s.infrastructure.(deployapi.LandingPolicyManager)
	if !ok {
		return errors.New("center: landing access management is unavailable")
	}
	var mode string
	if err := s.store.db.QueryRowContext(ctx, `SELECT mode FROM network_integrations WHERE kind='headscale' AND status='configured'`).Scan(&mode); err != nil || mode != "builtin" {
		return errors.New("center: landing requires managed Headscale access rules")
	}
	var before, after uint64
	if err := s.store.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='landing_policy_revision'`).Scan(&before); err != nil {
		return err
	}
	rules, err := s.store.landingAccessRules(ctx)
	if err != nil {
		return err
	}
	if err := s.store.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='landing_policy_revision'`).Scan(&after); err != nil {
		return err
	}
	if before != after {
		return errors.New("center: landing access configuration changed; retry")
	}
	return manager.ApplyLandingPolicy(ctx, deployapi.LandingPolicyRequest{Revision: before, Rules: rules})
}
