package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

// SelectMeridianLanding manages available exits. Account route grants remain
// the authority for who can use an exit; adding a server never grants access.
func (s *Store) SelectMeridianLanding(ctx context.Context, input LandingSelection) error {
	if len(input.NodeIDs) > landing.MaxServers {
		return errors.New("center: too many landing servers")
	}
	input.NodeIDs = append([]string{}, input.NodeIDs...)
	slices.Sort(input.NodeIDs)
	for i, id := range input.NodeIDs {
		if id == "" || strings.TrimSpace(id) != id || i > 0 && id == input.NodeIDs[i-1] {
			return errors.New("center: invalid landing server selection")
		}
		code, ok := regionCode(input.LandingRegionCodes[id])
		if !ok || code != input.LandingRegionCodes[id] {
			return errors.New("center: every landing server requires a region")
		}
	}
	if len(input.LandingRegionCodes) != len(input.NodeIDs) {
		return errors.New("center: invalid landing region")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := ensureMeridianManagementWritable(ctx, tx); err != nil {
		return err
	}
	if paused, err := executionClaimsPaused(ctx, tx); err != nil {
		return err
	} else if paused {
		return errExecutionBlocked
	}
	current, err := readLandingSelection(ctx, tx)
	if err != nil {
		return err
	}
	if current.Revision != input.Revision {
		return errors.New("center: landing selection changed; refresh and retry")
	}
	changedNodes := append(slices.Clone(current.NodeIDs), input.NodeIDs...)
	for _, id := range changedNodes {
		if slices.Contains(current.NodeIDs, id) && slices.Contains(input.NodeIDs, id) {
			continue
		}
		var blocked bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded')`, id).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return errExecutionBlocked
		}
	}
	input.RetiringNodeIDs = slices.DeleteFunc(slices.Clone(current.RetiringNodeIDs), func(id string) bool {
		return slices.Contains(input.NodeIDs, id)
	})
	for _, id := range input.NodeIDs {
		if slices.Contains(current.NodeIDs, id) {
			continue
		}
		var address string
		if err := tx.QueryRowContext(ctx, `SELECT profile.headscale_address FROM agents agent
			JOIN agent_network_profiles profile ON profile.agent_id=agent.id
			WHERE agent.id=? AND agent.status='active' AND agent.credential_revoked_at=''
			AND agent.tailscale_ownership='managed' AND agent.last_seen_at>?`, id,
			s.now().UTC().Add(-2*time.Minute).Format(time.RFC3339Nano)).Scan(&address); err != nil {
			return errors.New("center: select an online managed private-network node")
		}
		plan := &landing.ServerPlan{Revision: 1, Address: address}
		if err := plan.Validate(); err != nil {
			return errors.New("center: selected node has no usable private address")
		}
		var raw []byte
		err := tx.QueryRowContext(ctx, `SELECT desired_json FROM landing_server_states WHERE node_id=?`, id).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			var existing landing.ServerState
			if json.Unmarshal(raw, &existing) != nil || existing.Validate() != nil || existing.NodeID != id {
				return errLandingSourceReconciliation
			}
			if existing.Plan != nil {
				if existing.Plan.Address != address {
					return errLandingSourceReconciliation
				}
				// Restoring a draining server must not erase existing source grants.
				plan = existing.Plan
			}
		}
		if err := s.queueLandingServer(ctx, tx, id, plan); err != nil {
			return err
		}
		if err := s.refreshClientLandingSources(ctx, tx, id); err != nil {
			return err
		}
	}
	for _, id := range current.NodeIDs {
		if slices.Contains(input.NodeIDs, id) {
			continue
		}
		var grants int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_route_grants WHERE egress_node_id=? AND status<>'revoked'`, id).Scan(&grants); err != nil {
			return err
		}
		if grants != 0 {
			return errors.New("center: remove this landing server's account routes and wait for revocation before removing the server")
		}
		if err := s.queueLandingServer(ctx, tx, id, nil); err != nil {
			return err
		}
		input.RetiringNodeIDs = append(input.RetiringNodeIDs, id)
	}
	if input.LandingRegionCodes == nil {
		input.LandingRegionCodes = map[string]string{}
	}
	for _, id := range input.RetiringNodeIDs {
		if code := current.LandingRegionCodes[id]; code != "" {
			input.LandingRegionCodes[id] = code
		}
	}
	input.Revision++
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, landingSelectionKey, string(data)); err != nil {
		return err
	}
	return tx.Commit()
}

// A stop receipt is the final fence. Legacy grant tombstones retained after a
// completed cutover cannot keep an already stopped native exit in the UI.
func (s *Store) finalizeMeridianLandingRetirements(ctx context.Context, tx *sql.Tx) error {
	var writable bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM meridian_cutover WHERE id=1
		AND subscription_authority='meridian' AND state IN ('complete','not_required'))`).Scan(&writable); err != nil || !writable {
		return err
	}
	selection, err := readLandingSelection(ctx, tx)
	if err != nil {
		return err
	}
	remaining := []string{}
	for _, id := range selection.RetiringNodeIDs {
		var stopped bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM landing_server_states
			WHERE node_id=? AND status='stopped' AND applied_revision=desired_revision)
			AND NOT EXISTS(SELECT 1 FROM meridian_route_grants WHERE egress_node_id=? AND status<>'revoked')`, id, id).Scan(&stopped); err != nil {
			return err
		}
		if !stopped {
			remaining = append(remaining, id)
		} else {
			delete(selection.LandingRegionCodes, id)
		}
	}
	if slices.Equal(remaining, selection.RetiringNodeIDs) {
		return nil
	}
	selection.RetiringNodeIDs = remaining
	selection.Revision++
	data, err := json.Marshal(selection)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE settings SET value=? WHERE key=?`, string(data), landingSelectionKey)
	return err
}
