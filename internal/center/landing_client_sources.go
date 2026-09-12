package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/petauron/vastora/internal/landing"
)

// Regenerate protocol capabilities from the union of independent purposes.
// Old source snapshots stay referenced until route/session revocation succeeds.
func (s *Store) refreshClientLandingSources(ctx context.Context, tx *sql.Tx, landingID string) error {
	var data []byte
	if err := tx.QueryRowContext(ctx, `SELECT desired_json FROM landing_server_states WHERE node_id=?`, landingID).Scan(&data); errors.Is(err, sql.ErrNoRows) {
		return nil // Nothing remains to authorize on a removed service.
	} else if err != nil {
		return err
	}
	var state landing.ServerState
	if json.Unmarshal(data, &state) != nil || state.Validate() != nil {
		return errors.New("center: invalid landing service state")
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
		JOIN landing_client_capabilities c ON c.node_id=app.node_id
		JOIN landing_server_states s ON s.node_id=g.landing_node_id
		JOIN agents a ON a.id=app.node_id
		WHERE g.landing_node_id=? AND g.status<>'revoked' AND a.credential_revoked_at='' AND a.tailscale_ownership='managed'`, landingID)
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
			return errors.New("center: invalid grant source snapshot")
		}
		var actual, target landing.PeerIdentity
		var grant landing.ClientGrant
		if json.Unmarshal(current, &actual) != nil || json.Unmarshal(landingPeer, &target) != nil || json.Unmarshal(grantJSON, &grant) != nil {
			rows.Close()
			return errors.New("center: invalid current private identity")
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
	merged, err := landing.MergeSources(uses)
	if err != nil {
		return err
	}
	if slices.Equal(merged, state.Plan.Sources) {
		return nil
	}
	state.Plan.Sources = merged
	return s.queueLandingServer(ctx, tx, landingID, state.Plan)
}

func (s *Store) clientLandingRoutePrerequisites(ctx context.Context, tx *sql.Tx, state landing.DesiredState) (bool, error) {
	if state.Clients == nil {
		return true, nil
	}
	var generation int
	if err := tx.QueryRowContext(ctx, `SELECT generation FROM landing_client_capabilities WHERE node_id=?`, state.NodeID).Scan(&generation); err != nil || generation != landing.ClientRuntimeGeneration {
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
