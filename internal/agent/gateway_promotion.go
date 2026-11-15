package agent

import (
	"context"
	"errors"

	"github.com/petauron/vastora/internal/controlplane"
)

// PrepareGatewayPromotion uses the original pinned Center and machine credential.
// A local flag cannot issue this grant, rotate identity, or enroll a new machine.
func (c Client) PrepareGatewayPromotion(ctx context.Context, store *Store, id string) (controlplane.GatewayPromotion, error) {
	var result controlplane.GatewayPromotion
	connection, err := store.Connection(ctx)
	if err != nil {
		return result, err
	}
	connection, err = c.ensureConnectionPinned(ctx, store, connection)
	if err != nil {
		return result, err
	}
	request := struct {
		ID string `json:"id"`
	}{id}
	if err := c.callAgentRPC(ctx, connection, rpcGatewayPromotion, "", request, &result, 4096, false); err != nil {
		return result, err
	}
	if result.ID != id || result.AgentID != connection.AgentID || (result.State != "prepared" && result.State != "confirmed") {
		return result, errors.New("agent: Center did not confirm the exact gateway promotion")
	}
	return result, nil
}

// CommitGatewayPromotion changes only the purpose bit of the retained connection.
// It is called after authenticated preparation, with the Agent stopped. Existing
// identities, trust, encrypted credentials, applications and histories stay intact.
func (s *Store) CommitGatewayPromotion(ctx context.Context, grant controlplane.GatewayPromotion) error {
	if grant.ID == "" || grant.AgentID == "" || (grant.State != "prepared" && grant.State != "confirmed") {
		return errors.New("agent: authenticated promotion grant required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	var landingOnly bool
	if err := tx.QueryRowContext(ctx, `SELECT agent_id,landing_only FROM control_plane_connection WHERE id=1`).Scan(&id, &landingOnly); err != nil || id != grant.AgentID {
		return errors.New("agent: gateway promotion belongs to another enrollment")
	}
	if landingOnly {
		if _, err := tx.ExecContext(ctx, `UPDATE control_plane_connection SET landing_only=0 WHERE id=1 AND agent_id=? AND landing_only=1`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
