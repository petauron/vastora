package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
)

const agentGatewayPromotionSchema = `CREATE TABLE agent_gateway_promotions (
 agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE RESTRICT,
 id TEXT NOT NULL UNIQUE,
 credential_hash BLOB NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('authorized','prepared','confirmed')),
 authorized_at TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 confirmed_at TEXT NOT NULL DEFAULT ''
)`

// AuthorizeGatewayPromotion is an explicit local Center-operator action. It does
// not replace enrollment, credentials, private-network identity or application data.
func (s *Store) AuthorizeGatewayPromotion(ctx context.Context, agentID string) (controlplane.GatewayPromotion, error) {
	var result controlplane.GatewayPromotion
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || len(agentID) > 128 {
		return result, errors.New("center: exact existing Agent required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var raw []byte
	var binding []byte
	if err := tx.QueryRowContext(ctx, `SELECT capabilities_json,credential_hash FROM agents WHERE id=? AND status='active' AND credential_revoked_at=''`, agentID).Scan(&raw, &binding); err != nil {
		return result, errors.New("center: active enrolled Agent required")
	}
	var previous controlplane.GatewayPromotion
	var expires string
	err = tx.QueryRowContext(ctx, `SELECT id,state,expires_at FROM agent_gateway_promotions WHERE agent_id=? AND credential_hash=?`, agentID, binding).Scan(&previous.ID, &previous.State, &expires)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	deadline, _ := time.Parse(time.RFC3339Nano, expires)
	if err == nil && (previous.State != "authorized" || s.now().UTC().Before(deadline)) {
		previous.AgentID = agentID
		return previous, nil
	}
	var capabilities NodeCapabilities
	if json.Unmarshal(raw, &capabilities) != nil || !capabilities.LandingOnly || capabilities.Docker || capabilities.Gateway || capabilities.Tunnel {
		return result, errors.New("center: unchanged landing-only enrollment required")
	}
	if err := gatewayPromotionIdle(ctx, tx, agentID); err != nil {
		return result, err
	}
	id, err := randomToken(18)
	if err != nil {
		return result, err
	}
	now := s.now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_gateway_promotions(agent_id,id,credential_hash,state,authorized_at,expires_at) VALUES(?,?,?,'authorized',?,?) ON CONFLICT(agent_id) DO UPDATE SET id=excluded.id,credential_hash=excluded.credential_hash,state='authorized',authorized_at=excluded.authorized_at,expires_at=excluded.expires_at,confirmed_at=''`, agentID, id, binding, now.Format(time.RFC3339Nano), now.Add(15*time.Minute).Format(time.RFC3339Nano))
	if err != nil {
		return result, err
	}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, "node.gateway-promotion", 1, "queued", `{"source":"local-center-operator","purpose":"worker,gateway","phase":"authorized"}`); err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return controlplane.GatewayPromotion{ID: id, AgentID: agentID, State: "authorized"}, nil
}

func gatewayPromotionIdle(ctx context.Context, tx *sql.Tx, id string) error {
	var busy bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND state<>'succeeded' AND disposition='') OR EXISTS(SELECT 1 FROM deployments WHERE agent_id=? AND (state IN ('pending','running') OR reconciliation_required=1)) OR EXISTS(SELECT 1 FROM agent_updates WHERE agent_id=? AND state IN ('pending','running','installing'))`, id, id, id).Scan(&busy)
	if err != nil {
		return err
	}
	if blocked, err := agentReinstallBlocked(ctx, tx, id); err != nil {
		return err
	} else if blocked || busy {
		return errors.New("center: finish existing node operations before purpose promotion")
	}
	return nil
}

// PrepareGatewayPromotion is authenticated with the unchanged machine identity.
// Once prepared, a lost reply is retried with the same ID; it never re-enrolls.
func (s *Store) PrepareGatewayPromotion(ctx context.Context, agentID, credential, id string) (controlplane.GatewayPromotion, error) {
	var result controlplane.GatewayPromotion
	if err := s.authenticateAgent(ctx, agentID, credential); err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var expires string
	if err := tx.QueryRowContext(ctx, `SELECT p.id,p.state,p.expires_at FROM agent_gateway_promotions p JOIN agents a ON a.id=p.agent_id AND a.credential_hash=p.credential_hash WHERE p.agent_id=? AND p.id=? AND p.credential_hash=? AND a.status='active' AND a.credential_revoked_at=''`, agentID, id, tokenHash(credential)).Scan(&result.ID, &result.State, &expires); err != nil {
		return result, errors.New("center: operator-authorized promotion for this machine required")
	}
	if result.State == "authorized" {
		deadline, err := time.Parse(time.RFC3339Nano, expires)
		if err != nil || !s.now().UTC().Before(deadline) {
			return result, errors.New("center: promotion authorization expired before preparation")
		}
		if err := gatewayPromotionIdle(ctx, tx, agentID); err != nil {
			return result, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_gateway_promotions SET state='prepared' WHERE agent_id=? AND id=? AND state='authorized'`, agentID, id); err != nil {
			return result, err
		}
		result.State = "prepared"
		if err := s.recordTaskEvent(ctx, tx, id, agentID, "node.gateway-promotion", 1, "claimed", "authenticated machine accepted operator authorization; phase=prepared"); err != nil {
			return result, err
		}
	}
	result.AgentID = agentID
	return result, tx.Commit()
}

func gatewayPromotionPrepared(ctx context.Context, q networkQueryer, id string) (bool, error) {
	var prepared bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_gateway_promotions p JOIN agents a ON a.id=p.agent_id AND a.credential_hash=p.credential_hash WHERE p.agent_id=? AND p.state='prepared')`, id).Scan(&prepared)
	return prepared, err
}

func (s *Store) acceptsGatewayPromotionHeartbeat(ctx context.Context, id string, h NodeHeartbeat) (bool, error) {
	if !h.Startup || h.Capabilities.LandingOnly || !h.Capabilities.Docker || !h.Capabilities.Gateway || h.Capabilities.Tunnel || len(h.Roles) != 2 || !slices.Contains(h.Roles, "worker") || !slices.Contains(h.Roles, "gateway") {
		return false, nil
	}
	return gatewayPromotionPrepared(ctx, s.db, id)
}

func (s *Store) confirmGatewayPromotion(ctx context.Context, tx *sql.Tx, id, credential string) error {
	result, err := tx.ExecContext(ctx, `UPDATE agent_gateway_promotions SET state='confirmed',confirmed_at=? WHERE agent_id=? AND credential_hash=? AND state='prepared'`, s.now().UTC().Format(time.RFC3339Nano), id, tokenHash(credential))
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errors.New("center: promotion authorization changed before confirmation")
	}
	var promotionID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM agent_gateway_promotions WHERE agent_id=?`, id).Scan(&promotionID); err != nil {
		return err
	}
	return s.recordTaskEvent(ctx, tx, promotionID, id, "node.gateway-promotion", 1, "succeeded", "new worker/gateway startup confirmed; machine identity retained")
}
