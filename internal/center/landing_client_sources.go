package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
)

var errLandingSourceReconciliation = errors.New("center: landing source authorization requires explicit repair")

const landingSourceReconciliationMessage = "Landing source authorization requires explicit repair; the previous applied plan is unchanged."

// Freeze the shared landing authorization writer only during the authority
// handover. After completion, Meridian grants still need that same queue.
func meridianCutoverBlocksLandingChanges(ctx context.Context, queryer networkQueryer) (bool, error) {
	var blocked bool
	err := queryer.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM meridian_cutover
		WHERE id=1 AND state IN ('backup','import','publish','project','verify','retire'))`).Scan(&blocked)
	return blocked, err
}

// Regenerate protocol capabilities from the union of independent purposes.
// Old source snapshots stay referenced until route/session revocation succeeds.
func (s *Store) refreshClientLandingSources(ctx context.Context, tx *sql.Tx, landingID string) error {
	if blocked, err := meridianCutoverBlocksLandingChanges(ctx, tx); err != nil {
		return err
	} else if blocked {
		return nil
	}
	var data []byte
	if err := tx.QueryRowContext(ctx, `SELECT desired_json FROM landing_server_states WHERE node_id=?`, landingID).Scan(&data); errors.Is(err, sql.ErrNoRows) {
		return nil // Nothing remains to authorize on a removed service.
	} else if err != nil {
		return err
	}
	// An unresolved execution on the landing node fences any mutation. Keep
	// that error authoritative even if a stored plan is malformed, while a
	// removed service can still return above without touching the fence.
	var blocked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded')`, landingID).Scan(&blocked); err != nil {
		return err
	}
	var state landing.ServerState
	if json.Unmarshal(data, &state) != nil || state.Validate() != nil || state.NodeID != landingID {
		if blocked {
			return errExecutionBlocked
		}
		return errors.Join(errLandingSourceReconciliation, errors.New("center: invalid landing service state"))
	}
	if state.Plan == nil {
		return nil
	}
	uses := []landing.AuthorizedNode{}
	rows, err := tx.QueryContext(ctx, `SELECT source_address FROM landing_proxy_states WHERE landing_node_id=? AND json_extract(desired_json,'$.proxy') IS NOT NULL UNION SELECT source_address FROM landing_proxy_retirements WHERE landing_node_id=?`, landingID, landingID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			rows.Close()
			return err
		}
		uses = append(uses, landing.AuthorizedNode{Address: address})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `SELECT g.source_peer_json,c.peer_json,s.peer_json,g.grant_json
		FROM landing_client_grants g JOIN applications app ON app.id=g.application_id
		JOIN agent_private_peer_capabilities c ON c.node_id=app.node_id
		JOIN landing_server_states s ON s.node_id=g.landing_node_id
		JOIN agents a ON a.id=app.node_id
		WHERE g.landing_node_id=? AND g.status<>'revoked' AND a.credential_revoked_at='' AND a.tailscale_ownership='managed'
		AND NOT EXISTS(SELECT 1 FROM meridian_endpoints endpoint
			WHERE endpoint.application_id=g.application_id AND endpoint.service_id=g.service_id AND endpoint.legacy_retired=1)`, landingID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw, current, landingPeer, grantJSON []byte
		if err := rows.Scan(&raw, &current, &landingPeer, &grantJSON); err != nil {
			rows.Close()
			return err
		}
		var peer landing.PeerIdentity
		if json.Unmarshal(raw, &peer) != nil {
			rows.Close()
			return errors.Join(errLandingSourceReconciliation, errors.New("center: invalid grant source snapshot"))
		}
		var actual, target landing.PeerIdentity
		var grant landing.ClientGrant
		if json.Unmarshal(current, &actual) != nil || json.Unmarshal(landingPeer, &target) != nil || json.Unmarshal(grantJSON, &grant) != nil {
			rows.Close()
			return errors.Join(errLandingSourceReconciliation, errors.New("center: invalid current private identity"))
		}
		if actual != peer || target != grant.Peer {
			continue
		}
		uses = append(uses, landing.AuthorizedNode{Address: peer.Address, TCPOnly: true})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// The imported/explicit entry identity is authority. A heartbeat may report
	// a different capability, but must never silently repin a live source grant.
	// Keep authorization through quota changes, health loss and revocation until
	// the entry confirms removal and the grant becomes revoked.
	rows, err = tx.QueryContext(ctx, `SELECT DISTINCT endpoint.source_peer_json,agent.id,capability.peer_json
		FROM meridian_route_grants grant_row
		JOIN meridian_endpoints endpoint ON endpoint.id=grant_row.endpoint_id
		JOIN applications application ON application.id=endpoint.application_id
		JOIN agents agent ON agent.id=application.node_id
		LEFT JOIN agent_private_peer_capabilities capability ON capability.node_id=agent.id
		WHERE grant_row.egress_node_id=?
		AND ((grant_row.enabled=1 AND grant_row.status<>'revoked') OR grant_row.status='revoking')
		AND agent.status='active' AND agent.credential_revoked_at='' AND agent.tailscale_ownership='managed'`, landingID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw, observedJSON []byte
		var entryID string
		if err := rows.Scan(&raw, &entryID, &observedJSON); err != nil {
			rows.Close()
			return err
		}
		var peer landing.PeerIdentity
		if json.Unmarshal(raw, &peer) != nil || (meridianruntime.Peer{EgressID: entryID, Identity: peer}).Validate() != nil {
			rows.Close()
			return errors.Join(errLandingSourceReconciliation, errors.New("center: invalid Meridian grant source snapshot"))
		}
		var observed landing.PeerIdentity
		if json.Unmarshal(observedJSON, &observed) == nil && (meridianruntime.Peer{EgressID: entryID, Identity: observed}).Validate() == nil && observed != peer {
			// Exact authenticated replacement evidence revokes the old address.
			// Never authorize the replacement or rewrite the durable pin here.
			continue
		}
		uses = append(uses, landing.AuthorizedNode{Address: peer.Address, TCPOnly: true})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	merged, err := landing.MergeSources(uses)
	if err != nil {
		return errors.Join(errLandingSourceReconciliation, err)
	}
	if slices.Equal(merged, state.Plan.Sources) {
		return nil
	}
	// A valid no-op refresh must not roll back an unrelated successful receipt.
	if blocked {
		return errExecutionBlocked
	}
	state.Plan.Sources = merged
	if err := state.Plan.Validate(); err != nil {
		return errors.Join(errLandingSourceReconciliation, err)
	}
	return s.queueLandingServer(ctx, tx, landingID, state.Plan)
}

func (s *Store) clientLandingRoutePrerequisites(ctx context.Context, tx *sql.Tx, state landing.DesiredState) (bool, error) {
	if state.Clients == nil {
		return true, nil
	}
	var generation int
	if err := tx.QueryRowContext(ctx, `SELECT generation FROM agent_private_peer_capabilities WHERE node_id=?`, state.NodeID).Scan(&generation); err != nil || generation != landing.ClientRuntimeGeneration {
		return false, nil
	}
	for _, grant := range state.Clients.Grants {
		if !grant.Enabled {
			continue
		}
		record, err := readLandingGrant(ctx, tx, grant.ID)
		if err != nil {
			return false, err
		}
		if err := s.checkClientLandingIdentity(ctx, tx, record); err != nil {
			return false, err
		}
		var ready int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM landing_server_states WHERE node_id=? AND status='ready' AND desired_revision=applied_revision`, record.LandingNodeID).Scan(&ready); err != nil {
			return false, err
		}
		if ready != 1 {
			return false, nil
		}
	}
	return true, nil
}
