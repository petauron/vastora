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

const meridianRouteSourceUnauthorized = "Waiting for the landing service to confirm this entry's private source authorization."

// Only a successful server receipt grants access. A newer desired plan may
// remove permission, but cannot grant it before the landing Agent applies it.
// An additive update therefore keeps previously authorized entries available.
func meridianLandingSourceAuthorized(appliedJSON, desiredJSON []byte, nodeID string, revision uint64, source landing.PeerIdentity, peer landing.PeerIdentity) bool {
	if revision == 0 || (meridianruntime.Peer{EgressID: "entry-source", Identity: source}).Validate() != nil ||
		(meridianruntime.Peer{EgressID: nodeID, Identity: peer}).Validate() != nil {
		return false
	}
	var applied, desired landing.ServerState
	if json.Unmarshal(appliedJSON, &applied) != nil || applied.Validate() != nil || applied.NodeID != nodeID || applied.Revision != revision || applied.Plan == nil ||
		json.Unmarshal(desiredJSON, &desired) != nil || desired.Validate() != nil || desired.NodeID != nodeID || desired.Plan == nil ||
		applied.Plan.Address != peer.Address || desired.Plan.Address != peer.Address {
		return false
	}
	return meridianServerAuthorizesSource(appliedJSON, nodeID, source.Address) && meridianServerAuthorizesSource(desiredJSON, nodeID, source.Address)
}

func meridianServerAuthorizesSource(raw []byte, nodeID, sourceAddress string) bool {
	var state landing.ServerState
	if json.Unmarshal(raw, &state) != nil || state.Validate() != nil || state.NodeID != nodeID || state.Plan == nil {
		return false
	}
	return slices.ContainsFunc(state.Plan.Sources, func(source landing.AuthorizedNode) bool { return source.Address == sourceAddress })
}

// The server peer often stays identical when new entry sources are applied.
// Wake only the entries whose TCP authorization changed, not every account
// sharing this landing. Peer identity changes use their separate invalidation.
func (s *Store) markMeridianLandingAuthorizationChanged(ctx context.Context, tx *sql.Tx, nodeID string, previousAppliedJSON []byte, nowText string) error {
	var appliedJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT applied_json FROM landing_server_states WHERE node_id=?`, nodeID).Scan(&appliedJSON); err != nil {
		return err
	}
	var applied landing.ServerState
	if json.Unmarshal(appliedJSON, &applied) != nil || applied.Validate() != nil || applied.NodeID != nodeID {
		return errors.New("center: invalid applied landing authorization")
	}
	rows, err := tx.QueryContext(ctx, `SELECT grant_row.id,grant_row.endpoint_id,endpoint.source_peer_json
		FROM meridian_route_grants grant_row JOIN meridian_endpoints endpoint ON endpoint.id=grant_row.endpoint_id
		WHERE grant_row.egress_node_id=? AND grant_row.enabled=1 AND grant_row.status NOT IN ('revoked','revoking') AND endpoint.status<>'retired'
		ORDER BY grant_row.id`, nodeID)
	if err != nil {
		return err
	}
	grantIDs, endpointIDs := []string{}, []string{}
	for rows.Next() {
		var grantID, endpointID string
		var sourceJSON []byte
		if err := rows.Scan(&grantID, &endpointID, &sourceJSON); err != nil {
			rows.Close()
			return err
		}
		var source landing.PeerIdentity
		if json.Unmarshal(sourceJSON, &source) != nil || (meridianruntime.Peer{EgressID: "entry-source", Identity: source}).Validate() != nil {
			continue
		}
		if meridianServerAuthorizesSource(previousAppliedJSON, nodeID, source.Address) == meridianServerAuthorizesSource(appliedJSON, nodeID, source.Address) {
			continue
		}
		grantIDs = append(grantIDs, grantID)
		if !slices.Contains(endpointIDs, endpointID) {
			endpointIDs = append(endpointIDs, endpointID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, endpointID := range endpointIDs {
		if err := s.ensureMeridianSubscriptionSnapshotsForEndpointInTx(ctx, tx, endpointID); err != nil {
			return err
		}
	}
	for _, grantID := range grantIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET desired_revision=desired_revision+1,runtime_healthy=0,health_expires_unix_ms=0,status='pending',last_error='',updated_at=? WHERE id=?`, nowText, grantID); err != nil {
			return err
		}
	}
	for _, endpointID := range endpointIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET desired_revision=desired_revision+1,runtime_healthy=0,status='pending',last_error='',updated_at=? WHERE id=?`, nowText, endpointID); err != nil {
			return err
		}
	}
	return nil
}
