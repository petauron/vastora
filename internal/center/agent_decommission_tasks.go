package center

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

func agentDecommissionTaskID(agentID string) string {
	return "agent-decommission-" + agentID
}

func (s *Store) queueAgentDecommission(ctx context.Context, agentID string, deleteData bool) error {
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_decommissions(agent_id, delete_data, state, created_at, updated_at)
		VALUES(?, ?, 'pending', ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET delete_data = excluded.delete_data, state = CASE WHEN agent_decommissions.state IN ('cleaning', 'succeeded') THEN agent_decommissions.state ELSE 'pending' END,
		callback_token_hash = CASE WHEN agent_decommissions.state IN ('cleaning', 'succeeded') THEN agent_decommissions.callback_token_hash ELSE X'' END,
		lease_expires_at = '', last_error = '', updated_at = excluded.updated_at`, agentID, deleteData, now, now)
	if err != nil {
		return fmt.Errorf("center: queue Agent host cleanup: %w", err)
	}
	return s.recordStandaloneTaskEvent(ctx, agentDecommissionTaskID(agentID), agentID, "agent.decommission", 1, "queued", "host cleanup queued")
}

func (s *Store) beginAgentDecommission(ctx context.Context, agentID, credential, taskID string, expectedAttempt int64) error {
	if taskID != agentDecommissionTaskID(agentID) || expectedAttempt <= 0 {
		return errStaleTaskLease
	}
	if err := s.authenticateAgent(ctx, agentID, credential); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE agent_decommissions SET state = 'cleaning', lease_expires_at = '', updated_at = ?
		WHERE agent_id = ? AND state = 'running' AND attempt = ?`, now, agentID, expectedAttempt)
	if err != nil {
		return fmt.Errorf("center: begin Agent host cleanup handoff: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		var state string
		var attempt int64
		if err := tx.QueryRowContext(ctx, `SELECT state, attempt FROM agent_decommissions WHERE agent_id = ?`, agentID).Scan(&state, &attempt); err != nil || attempt != expectedAttempt || (state != "cleaning" && state != "succeeded") {
			return errStaleTaskLease
		}
		return tx.Commit()
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "agent.decommission", 1, "claimed", fmt.Sprintf("persistent host cleanup helper started attempt %d", expectedAttempt)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) claimAgentDecommission(ctx context.Context, tx *sql.Tx, agentID string) (*AgentTask, error) {
	var deleteData bool
	var attempt int64
	err := tx.QueryRowContext(ctx, `SELECT delete_data, attempt FROM agent_decommissions WHERE agent_id = ? AND state IN ('pending', 'failed')`, agentID).Scan(&deleteData, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("center: read Agent host cleanup: %w", err)
	}
	taskID := agentDecommissionTaskID(agentID)
	callbackURL, err := s.agentDecommissionCallbackURL(ctx, tx, taskID)
	if err != nil {
		return nil, err
	}
	callbackToken, err := randomToken(32)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE agent_decommissions SET state = 'running', attempt = attempt + 1, callback_token_hash = ?, lease_expires_at = ?, last_error = '', updated_at = ?
		WHERE agent_id = ? AND state IN ('pending', 'failed') AND attempt = ?`, tokenHash(callbackToken), now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), agentID, attempt)
	if err != nil {
		return nil, fmt.Errorf("center: claim Agent host cleanup: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, errors.New("center: Agent host cleanup changed while claiming")
	}
	task := &AgentTask{Kind: "agent.decommission", ID: taskID, Attempt: attempt + 1, DeleteData: deleteData, DecommissionCallbackURL: callbackURL, DecommissionCallbackToken: callbackToken, Revision: 1}
	if err := s.recordTaskEvent(ctx, tx, task.ID, agentID, task.Kind, 1, "claimed", fmt.Sprintf("attempt %d", task.Attempt)); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *Store) agentDecommissionCallbackURL(ctx context.Context, tx *sql.Tx, taskID string) (string, error) {
	var endpoint string
	err := tx.QueryRowContext(ctx, `SELECT endpoint FROM network_integrations WHERE kind = 'headscale' AND status = 'configured'`).Scan(&endpoint)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, agentConnectURLSetting).Scan(&endpoint)
		if err == nil {
			endpoint, err = NormalizeAgentConnectURL(endpoint)
		}
	} else if err == nil {
		endpoint, err = normalizeHeadscaleEndpoint(endpoint)
	}
	if err != nil {
		return "", fmt.Errorf("center: resolve public Agent cleanup callback: %w", err)
	}
	return strings.TrimRight(endpoint, "/") + "/api/v1/agent-decommission-results/" + url.PathEscape(taskID), nil
}

func (s *Store) completeAgentDecommissionCallback(ctx context.Context, taskID, token string, expectedAttempt int64) error {
	taskID = strings.TrimSpace(taskID)
	token = strings.TrimSpace(token)
	if taskID == "" || token == "" || expectedAttempt <= 0 {
		return errors.New("center: invalid Agent host cleanup callback")
	}
	var agentID, state string
	var attempt int64
	var expectedTokenHash []byte
	err := s.db.QueryRowContext(ctx, `SELECT agent_id, state, attempt, callback_token_hash FROM agent_decommissions
		WHERE 'agent-decommission-' || agent_id = ?`, taskID).Scan(&agentID, &state, &attempt, &expectedTokenHash)
	if err != nil || attempt != expectedAttempt || (state != "cleaning" && state != "succeeded") || subtle.ConstantTimeCompare(expectedTokenHash, tokenHash(token)) != 1 {
		return errors.New("center: invalid Agent host cleanup callback")
	}
	return s.completeAgentDecommission(ctx, agentID, expectedAttempt)
}

func (s *Store) completeAgentDecommission(ctx context.Context, agentID string, expectedAttempt int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE agent_decommissions SET state = 'succeeded', lease_expires_at = '', last_error = '', updated_at = ?
		WHERE agent_id = ? AND state = 'cleaning' AND attempt = ?`, s.now().UTC().Format(time.RFC3339Nano), agentID, expectedAttempt)
	if err != nil {
		return fmt.Errorf("center: complete Agent host cleanup: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		var existingState string
		var attempt int64
		if err := tx.QueryRowContext(ctx, `SELECT state, attempt FROM agent_decommissions WHERE agent_id = ?`, agentID).Scan(&existingState, &attempt); err != nil || attempt != expectedAttempt || existingState != "succeeded" {
			return errors.New("center: Agent host cleanup task is stale")
		}
		return tx.Commit()
	}
	if err := s.recordTaskEvent(ctx, tx, agentDecommissionTaskID(agentID), agentID, "agent.decommission", 1, "succeeded", ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) failAgentDecommissionClaim(ctx context.Context, agentID string, expectedAttempt int64, taskError string) error {
	taskError = strings.TrimSpace(taskError)
	if taskError == "" {
		taskError = "Agent could not start its persistent host cleanup helper"
	}
	if len(taskError) > 1024 {
		taskError = taskError[:1024]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE agent_decommissions SET state = 'failed', callback_token_hash = X'', lease_expires_at = '', last_error = ?, updated_at = ?
		WHERE agent_id = ? AND state = 'running' AND attempt = ?`, taskError, s.now().UTC().Format(time.RFC3339Nano), agentID, expectedAttempt)
	if err != nil {
		return fmt.Errorf("center: fail Agent host cleanup claim: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		var state string
		var attempt int64
		if err := tx.QueryRowContext(ctx, `SELECT state, attempt FROM agent_decommissions WHERE agent_id = ?`, agentID).Scan(&state, &attempt); err != nil || attempt != expectedAttempt || state != "failed" {
			return errStaleTaskLease
		}
		return tx.Commit()
	}
	if err := s.recordTaskEvent(ctx, tx, agentDecommissionTaskID(agentID), agentID, "agent.decommission", 1, "failed", taskError); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) waitForAgentDecommissions(ctx context.Context, agents []AgentView, progress func(string)) error {
	pending := make(map[string]string, len(agents))
	for _, agent := range agents {
		pending[agent.ID] = agent.Name
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for len(pending) != 0 {
		for agentID, name := range pending {
			var state, taskError string
			if err := s.db.QueryRowContext(ctx, `SELECT state, last_error FROM agent_decommissions WHERE agent_id = ?`, agentID).Scan(&state, &taskError); err != nil {
				return fmt.Errorf("center: inspect Agent host cleanup for %s: %w", name, err)
			}
			switch state {
			case "succeeded":
				delete(pending, agentID)
				if progress != nil {
					progress(fmt.Sprintf("Host cleanup acknowledged: %s (%s)", name, agentID))
				}
			case "failed":
				return fmt.Errorf("center: Agent host cleanup failed for %s (%s): %s", name, agentID, taskError)
			}
		}
		if len(pending) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("center: Agent host cleanup did not finish: %w", ctx.Err())
		case <-ticker.C:
		}
	}
	return nil
}
