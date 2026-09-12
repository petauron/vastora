package center

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

func recordLandingClientRuntime(ctx context.Context, tx *sql.Tx, nodeID string, runtime *landing.ClientRuntime, now time.Time) error {
	if runtime == nil || runtime.Generation != landing.ClientRuntimeGeneration || runtime.Peer.ID == "" || runtime.Peer.PublicKey == "" || (landing.ServerPlan{Revision: 1, Address: runtime.Peer.Address}).Validate() != nil {
		_, err := tx.ExecContext(ctx, `DELETE FROM landing_client_capabilities WHERE node_id=?`, nodeID)
		return err
	}
	var address, ownership string
	if err := tx.QueryRowContext(ctx, `SELECT p.headscale_address,a.tailscale_ownership FROM agent_network_profiles p JOIN agents a ON a.id=p.agent_id WHERE p.agent_id=?`, nodeID).Scan(&address, &ownership); err != nil || address != runtime.Peer.Address || ownership != "managed" {
		_, err := tx.ExecContext(ctx, `DELETE FROM landing_client_capabilities WHERE node_id=?`, nodeID)
		return err
	}
	data, _ := json.Marshal(runtime.Peer)
	_, err := tx.ExecContext(ctx, `INSERT INTO landing_client_capabilities(node_id,generation,peer_json,observed_at) VALUES(?,?,?,?) ON CONFLICT(node_id) DO UPDATE SET generation=excluded.generation,peer_json=excluded.peer_json,observed_at=excluded.observed_at`, nodeID, runtime.Generation, data, now.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) observeLandingAccounts(ctx context.Context, tx *sql.Tx, commandID string, result *ThreeXUIClientCommandResult) error {
	if !result.ClientsObserved {
		return nil
	}
	var controllerID string
	if err := tx.QueryRowContext(ctx, `SELECT application_id FROM application_commands WHERE id=?`, commandID).Scan(&controllerID); err != nil {
		return err
	}
	for _, client := range result.Clients {
		decoded, err := hex.DecodeString(client.ID)
		if err != nil || len(decoded) != 32 || strings.HasPrefix(client.Email, "vastora-combination-") {
			continue
		}
		var priorID string
		err = tx.QueryRowContext(ctx, `SELECT id FROM three_x_ui_client_accounts WHERE controller_id=? AND email=?`, controllerID, client.Email).Scan(&priorID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if priorID != "" && priorID != client.ID {
			// Preserve the old stable identity and its grants, but release the
			// display-name index. One externally recreated client must not make
			// every other client's inventory unusable. The retired name cannot
			// be accepted by the ordinary client API (over its 64-rune limit).
			if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_client_accounts SET email=? WHERE id=?`, "retired:"+priorID, priorID); err != nil {
				return err
			}
		}
		encoded, _ := json.Marshal(client)
		written, err := tx.ExecContext(ctx, `INSERT INTO three_x_ui_client_accounts(id,controller_id,email,metadata_json,observed_at) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET email=excluded.email,metadata_json=excluded.metadata_json,observed_at=excluded.observed_at WHERE controller_id=excluded.controller_id`, client.ID, controllerID, client.Email, encoded, s.now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		if count, err := written.RowsAffected(); err != nil || count != 1 {
			return errors.New("center: client belongs to another subscription controller; reconcile ownership first")
		}
	}
	return nil
}
