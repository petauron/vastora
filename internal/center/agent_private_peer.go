package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

// The private peer observation is authenticated by the Agent heartbeat.
// Meridian pins this identity when it first authorizes an entry-to-egress
// route; a later observation cannot silently replace the pin.
func recordAgentPrivatePeer(ctx context.Context, tx *sql.Tx, nodeID string, runtime *landing.ClientRuntime, now time.Time) error {
	if runtime == nil || runtime.Generation != landing.ClientRuntimeGeneration || runtime.Peer.ID == "" || runtime.Peer.PublicKey == "" || (landing.ServerPlan{Revision: 1, Address: runtime.Peer.Address}).Validate() != nil {
		_, err := tx.ExecContext(ctx, `DELETE FROM agent_private_peer_capabilities WHERE node_id=?`, nodeID)
		return err
	}
	var address, ownership string
	if err := tx.QueryRowContext(ctx, `SELECT p.headscale_address,a.tailscale_ownership FROM agent_network_profiles p JOIN agents a ON a.id=p.agent_id WHERE p.agent_id=?`, nodeID).Scan(&address, &ownership); err != nil || address != runtime.Peer.Address || ownership != "managed" {
		_, err := tx.ExecContext(ctx, `DELETE FROM agent_private_peer_capabilities WHERE node_id=?`, nodeID)
		return err
	}
	data, _ := json.Marshal(runtime.Peer)
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_private_peer_capabilities(node_id,generation,peer_json,observed_at) VALUES(?,?,?,?) ON CONFLICT(node_id) DO UPDATE SET generation=excluded.generation,peer_json=excluded.peer_json,observed_at=excluded.observed_at`, nodeID, runtime.Generation, data, now.UTC().Format(time.RFC3339Nano))
	return err
}
