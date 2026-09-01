package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

type AgentUpdateView struct {
	ID            string    `json:"id"`
	TargetVersion string    `json:"targetVersion"`
	State         string    `json:"state"`
	LastError     string    `json:"lastError,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func isAgentUpdateTaskID(value string) bool {
	return strings.HasPrefix(value, "agent-update-")
}

func (s *Store) QueueAgentUpdate(ctx context.Context, agentID, targetVersion string) (AgentUpdateView, error) {
	agentID = strings.TrimSpace(agentID)
	targetVersion = strings.TrimPrefix(strings.TrimSpace(targetVersion), "v")
	if agentID == "" || targetVersion == "" || !semver.IsValid("v"+targetVersion) {
		return AgentUpdateView{}, errors.New("center: Agent and target version are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentUpdateView{}, err
	}
	defer tx.Rollback()
	var currentVersion, status, revokedAt, lastSeenAt string
	var supported bool
	if err := tx.QueryRowContext(ctx, `SELECT version, status, credential_revoked_at, last_seen_at, remote_update_supported FROM agents WHERE id = ?`, agentID).Scan(&currentVersion, &status, &revokedAt, &lastSeenAt, &supported); errors.Is(err, sql.ErrNoRows) {
		return AgentUpdateView{}, errors.New("center: Agent not found")
	} else if err != nil {
		return AgentUpdateView{}, fmt.Errorf("center: inspect Agent before update: %w", err)
	}
	if status != "active" || revokedAt != "" {
		return AgentUpdateView{}, errors.New("center: Agent is disabled")
	}
	if !supported {
		return AgentUpdateView{}, errors.New("center: Agent requires one manual update before Center-managed updates are available")
	}
	seen, err := time.Parse(time.RFC3339Nano, lastSeenAt)
	if err != nil || !seen.After(s.now().Add(-agentConnectedMaxAge)) {
		return AgentUpdateView{}, errors.New("center: Agent must be online before it can update")
	}
	if currentVersion == targetVersion {
		return AgentUpdateView{}, errors.New("center: Agent is already running the current Center version")
	}
	if semver.IsValid("v"+currentVersion) && semver.Compare("v"+targetVersion, "v"+currentVersion) <= 0 {
		return AgentUpdateView{}, errors.New("center: Agent updates must move forward")
	}
	var existing AgentUpdateView
	var existingUpdatedAt string
	err = tx.QueryRowContext(ctx, `SELECT id, target_version, state, last_error, updated_at FROM agent_updates WHERE agent_id = ? AND state IN ('pending', 'running', 'installing')`, agentID).Scan(&existing.ID, &existing.TargetVersion, &existing.State, &existing.LastError, &existingUpdatedAt)
	if err == nil {
		existing.UpdatedAt, err = time.Parse(time.RFC3339Nano, existingUpdatedAt)
		if err != nil {
			return AgentUpdateView{}, errors.New("center: stored Agent update timestamp is invalid")
		}
		if existing.TargetVersion != targetVersion {
			return AgentUpdateView{}, errors.New("center: another Agent update is already active")
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AgentUpdateView{}, fmt.Errorf("center: inspect active Agent update: %w", err)
	}
	token, err := randomToken(18)
	if err != nil {
		return AgentUpdateView{}, err
	}
	now := s.now().UTC()
	update := AgentUpdateView{ID: "agent-update-" + token, TargetVersion: targetVersion, State: "pending", UpdatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_updates(id, agent_id, target_version, state, created_at, updated_at) VALUES(?, ?, ?, 'pending', ?, ?)`, update.ID, agentID, targetVersion, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return AgentUpdateView{}, fmt.Errorf("center: queue Agent update: %w", err)
	}
	if err := s.recordTaskEvent(ctx, tx, update.ID, agentID, "agent.update", 1, "queued", "Agent update to "+targetVersion+" queued"); err != nil {
		return AgentUpdateView{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentUpdateView{}, err
	}
	return update, nil
}

func (s *Store) claimAgentUpdate(ctx context.Context, tx *sql.Tx, agentID string) (*AgentTask, error) {
	var id, targetVersion string
	var attempt int64
	err := tx.QueryRowContext(ctx, `SELECT id, target_version, attempt FROM agent_updates WHERE agent_id = ? AND state = 'pending' ORDER BY created_at, rowid LIMIT 1`, agentID).Scan(&id, &targetVersion, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("center: read pending Agent update: %w", err)
	}
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE agent_updates SET state = 'running', attempt = attempt + 1, lease_expires_at = ?, last_error = '', updated_at = ? WHERE id = ? AND agent_id = ? AND state = 'pending' AND attempt = ?`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id, agentID, attempt)
	if err != nil {
		return nil, fmt.Errorf("center: claim Agent update: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, errors.New("center: Agent update changed while claiming")
	}
	task := &AgentTask{Kind: "agent.update", ID: id, Attempt: attempt + 1, Revision: 1, TargetVersion: targetVersion}
	if err := s.recordTaskEvent(ctx, tx, task.ID, agentID, task.Kind, 1, "claimed", fmt.Sprintf("attempt %d", task.Attempt)); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *Store) beginAgentUpdate(ctx context.Context, agentID, credential, taskID string, expectedAttempt int64) error {
	if !isAgentUpdateTaskID(taskID) || expectedAttempt <= 0 {
		return errStaleTaskLease
	}
	if err := s.authenticateAgent(ctx, agentID, credential); err != nil {
		return err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE agent_updates SET state = 'installing', lease_expires_at = '', updated_at = ? WHERE id = ? AND agent_id = ? AND state = 'running' AND attempt = ?`, now, taskID, agentID, expectedAttempt)
	if err != nil {
		return fmt.Errorf("center: begin Agent update handoff: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed == 1 {
		return nil
	}
	var state string
	var attempt int64
	if err := s.db.QueryRowContext(ctx, `SELECT state, attempt FROM agent_updates WHERE id = ? AND agent_id = ?`, taskID, agentID).Scan(&state, &attempt); err != nil || attempt != expectedAttempt || (state != "installing" && state != "succeeded" && state != "failed") {
		return errStaleTaskLease
	}
	return nil
}

func (s *Store) completeAgentUpdate(ctx context.Context, agentID, taskID string, expectedAttempt int64, succeeded bool, taskError string) error {
	taskError = strings.TrimSpace(taskError)
	if len(taskError) > 1024 {
		taskError = taskError[:1024]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var targetVersion, currentState string
	var attempt int64
	if err := tx.QueryRowContext(ctx, `SELECT target_version, state, attempt FROM agent_updates WHERE id = ? AND agent_id = ?`, taskID, agentID).Scan(&targetVersion, &currentState, &attempt); err != nil {
		return errors.New("center: Agent update task not found")
	}
	if attempt != expectedAttempt || expectedAttempt <= 0 {
		return errors.New("center: Agent update task is stale")
	}
	desiredState := "succeeded"
	if !succeeded {
		desiredState = "failed"
		if taskError == "" {
			taskError = "Agent update failed"
		}
	}
	if currentState == desiredState {
		return tx.Commit()
	}
	if succeeded {
		var liveVersion, lastSeenAt string
		var supported bool
		if err := tx.QueryRowContext(ctx, `SELECT version, last_seen_at, remote_update_supported FROM agents WHERE id = ?`, agentID).Scan(&liveVersion, &lastSeenAt, &supported); err != nil {
			return errors.New("center: updated Agent did not reconnect")
		}
		seen, parseErr := time.Parse(time.RFC3339Nano, lastSeenAt)
		if parseErr != nil || liveVersion != targetVersion || !supported || !seen.After(s.now().Add(-agentConnectedMaxAge)) {
			return errors.New("center: updated Agent has not reconnected with the target version")
		}
		if currentState != "installing" {
			return errors.New("center: Agent update is not installing")
		}
	} else if currentState != "running" && currentState != "installing" {
		return errors.New("center: Agent update is not active")
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_updates SET state = ?, lease_expires_at = '', last_error = ?, updated_at = ? WHERE id = ? AND agent_id = ? AND state = ? AND attempt = ?`, desiredState, taskError, s.now().UTC().Format(time.RFC3339Nano), taskID, agentID, currentState, expectedAttempt)
	if err != nil {
		return fmt.Errorf("center: complete Agent update: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("center: Agent update changed before completion")
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "agent.update", 1, desiredState, taskError); err != nil {
		return err
	}
	return tx.Commit()
}
