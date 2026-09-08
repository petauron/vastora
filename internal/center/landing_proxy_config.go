package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

type LandingProxyInput struct {
	Enabled  bool   `json:"enabled"`
	Revision uint64 `json:"revision"`
}

func (s *Store) ConfigureLandingProxy(ctx context.Context, applicationID string, input LandingProxyInput) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var nodeID, address string
	if err := tx.QueryRowContext(ctx, `SELECT a.node_id,COALESCE(p.headscale_address,'') FROM applications a JOIN agents n ON n.id=a.node_id LEFT JOIN agent_network_profiles p ON p.agent_id=n.id
 WHERE a.id=? AND a.app_key='vastora-official/3x-ui' AND a.runtime='docker' AND n.status='active' AND n.credential_revoked_at=''`, applicationID).Scan(&nodeID, &address); err != nil {
		return errors.New("center: managed VLESS node is unavailable")
	}
	var revision uint64
	var owner, source string
	var encoded []byte
	err = tx.QueryRowContext(ctx, `SELECT desired_revision,landing_node_id,source_address,desired_json FROM landing_proxy_states WHERE node_id=?`, nodeID).Scan(&revision, &owner, &source, &encoded)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if revision != input.Revision {
		return errors.New("center: landing settings changed; refresh and retry")
	}
	var previous landing.DesiredState
	if len(encoded) > 0 && (json.Unmarshal(encoded, &previous) != nil || previous.Validate() != nil) {
		return errors.New("center: invalid saved landing settings")
	}
	if !input.Enabled && revision == 0 {
		return nil
	}
	if input.Enabled && previous.Proxy != nil {
		return nil
	}
	state := landing.DesiredState{NodeID: nodeID, Revision: revision + 1}
	serverRevision := int64(0)
	if input.Enabled {
		// Enabling requires a current private identity. Disabling uses the
		// saved ownership/source snapshot so a missing network profile cannot
		// prevent restoration of the application's original routing.
		if (landing.ServerPlan{Revision: 1, Address: address}).Validate() != nil {
			return errors.New("center: managed VLESS private address is unavailable")
		}
		selection, err := readLandingSelection(ctx, tx)
		if err != nil {
			return err
		}
		if selection.NodeID == "" || selection.NodeID == nodeID {
			return errors.New("center: choose a different landing node first")
		}
		owner, source = selection.NodeID, address
		var serverJSON, peerJSON []byte
		if err := tx.QueryRowContext(ctx, `SELECT desired_revision,desired_json,peer_json FROM landing_server_states WHERE node_id=? AND status='ready' AND desired_revision=applied_revision`, owner).Scan(&serverRevision, &serverJSON, &peerJSON); err != nil {
			return errors.New("center: wait for the landing node to finish configuration")
		}
		var server landing.ServerState
		var peer landing.PeerIdentity
		if json.Unmarshal(serverJSON, &server) != nil || server.Validate() != nil || server.Plan == nil || json.Unmarshal(peerJSON, &peer) != nil || peer.Address != server.Plan.Address || peer.ID == "" || peer.PublicKey == "" {
			return errors.New("center: landing private identity is unavailable")
		}
		rows, err := tx.QueryContext(ctx, `SELECT p.inbound_tag FROM three_x_ui_inbound_plans p JOIN services service ON service.id=p.service_id WHERE service.application_id=? AND service.app_protocol='vless/tcp/reality' AND service.status<>'stopped' ORDER BY p.inbound_tag`, applicationID)
		if err != nil {
			return err
		}
		var tags []string
		for rows.Next() {
			var tag string
			if err := rows.Scan(&tag); err != nil {
				rows.Close()
				return err
			}
			tags = append(tags, tag)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		state.Proxy = &landing.ProxyPlan{ApplicationID: applicationID, InboundTags: tags, Peer: peer}
		if err := state.Validate(); err != nil {
			return err
		}
		found := false
		for _, allowed := range server.Plan.Sources {
			if allowed.Address == source {
				found = true
			}
		}
		if !found {
			server.Plan.Sources = append(server.Plan.Sources, landing.AuthorizedNode{Address: source})
		}
		slices.SortFunc(server.Plan.Sources, func(a, b landing.AuthorizedNode) int {
			if a.Address < b.Address {
				return -1
			}
			if a.Address > b.Address {
				return 1
			}
			return 0
		})
		if err := s.queueLandingServer(ctx, tx, owner, server.Plan); err != nil {
			return err
		}
		serverRevision++
	}
	if err := state.Validate(); err != nil {
		return err
	}
	encoded, err = json.Marshal(state)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO landing_proxy_states(node_id,application_id,landing_node_id,server_revision,source_address,desired_revision,desired_json,status,updated_at)
 VALUES(?,?,?,?,?,?,?,'pending',?) ON CONFLICT(node_id) DO UPDATE SET application_id=excluded.application_id,landing_node_id=excluded.landing_node_id,
 server_revision=excluded.server_revision,source_address=excluded.source_address,desired_revision=excluded.desired_revision,desired_json=excluded.desired_json,status='pending',lease_expires_at='',last_error='',updated_at=excluded.updated_at`, nodeID, applicationID, owner, serverRevision, source, state.Revision, encoded, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, landingProxyTaskID(nodeID, int64(state.Revision)), nodeID, "landing.proxy.apply", int64(state.Revision), "queued", "landing settings queued"); err != nil {
		return err
	}
	return tx.Commit()
}

// Called only after the proxy confirms restoration. Use the stored source
// snapshot, not the node's possibly changed network profile.
func (s *Store) removeLandingProxySource(ctx context.Context, tx *sql.Tx, nodeID string) error {
	var owner, source string
	if err := tx.QueryRowContext(ctx, `SELECT landing_node_id,source_address FROM landing_proxy_states WHERE node_id=?`, nodeID).Scan(&owner, &source); err != nil {
		return err
	}
	var encoded []byte
	if err := tx.QueryRowContext(ctx, `SELECT desired_json FROM landing_server_states WHERE node_id=?`, owner).Scan(&encoded); err != nil {
		return err
	}
	var server landing.ServerState
	if json.Unmarshal(encoded, &server) != nil || server.Validate() != nil {
		return errors.New("center: invalid landing source configuration")
	}
	if server.Plan == nil {
		return nil
	}
	count := len(server.Plan.Sources)
	server.Plan.Sources = slices.DeleteFunc(server.Plan.Sources, func(node landing.AuthorizedNode) bool { return node.Address == source })
	if len(server.Plan.Sources) == count {
		return nil
	}
	return s.queueLandingServer(ctx, tx, owner, server.Plan)
}
