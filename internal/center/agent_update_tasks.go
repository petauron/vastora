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

type AgentUpdateRolloutStatus struct {
	TargetVersion string `json:"targetVersion"`
	Total         int    `json:"total"`
	Updated       int    `json:"updated"`
	Updating      int    `json:"updating"`
	Pending       int    `json:"pending"`
	Failed        int    `json:"failed"`
	Offline       int    `json:"offline"`
	Manual        int    `json:"manual"`
}

// This bounds the busy indicator, not the durable installation or its backup.
// A helper that already owns a migration must remain recoverable after the UI
// reports that it needs attention.
const agentUpdateProgressTimeout = 5 * time.Minute

func isAgentUpdateTaskID(value string) bool {
	return strings.HasPrefix(value, "agent-update-")
}

func (s *Store) QueueAgentUpdate(ctx context.Context, agentID, targetVersion string) (AgentUpdateView, error) {
	return s.queueAgentUpdate(ctx, agentID, targetVersion, nil)
}

type AgentUpdateRecoveryInput struct {
	FailedUpdateID   string `json:"failedUpdateId"`
	ExecutionStopped bool   `json:"executionStopped"`
	Note             string `json:"note"`
	adminID          string
}

func (s *Store) queueAgentUpdate(ctx context.Context, agentID, targetVersion string, recovery *AgentUpdateRecoveryInput) (AgentUpdateView, error) {
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
	var lastState, lastID string
	err = tx.QueryRowContext(ctx, `SELECT state,id FROM agent_updates WHERE agent_id=? ORDER BY created_at DESC,rowid DESC LIMIT 1`, agentID).Scan(&lastState, &lastID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AgentUpdateView{}, err
	}
	if recovery != nil {
		if lastState != "failed" || recovery.FailedUpdateID != lastID || !recovery.ExecutionStopped || strings.TrimSpace(recovery.Note) == "" || len(recovery.Note) > 1024 {
			return AgentUpdateView{}, errors.New("center: confirm the exact failed update is stopped and record recovery verification")
		}
		var admin, blocked bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admins WHERE id=?)`, recovery.adminID).Scan(&admin); err != nil || !admin {
			return AgentUpdateView{}, errors.New("center: administrator authorization required")
		}
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded') OR EXISTS(SELECT 1 FROM agents WHERE id=? AND runtime_recovery<>'')`, agentID, agentID).Scan(&blocked); err != nil {
			return AgentUpdateView{}, err
		}
		if blocked {
			return AgentUpdateView{}, errors.New("center: resolve outstanding execution and runtime recovery before updating")
		}
		if paused, err := executionClaimsPaused(ctx, tx); err != nil {
			return AgentUpdateView{}, err
		} else if paused {
			return AgentUpdateView{}, errExecutionBlocked
		}
		// Keep the original failure immutable. Recovery authorizes a new task,
		// never replay or a fabricated success for the old attempt.
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,json_object('actor',?,'note',?,'targetVersion',?,'recoveredAt',?))`, "agent-update-recovery:"+lastID, recovery.adminID, recovery.Note, targetVersion, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
			return AgentUpdateView{}, err
		}
	}
	if lastState == "failed" && recovery == nil {
		var abandoned bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_executions e JOIN agent_updates u ON u.id=e.task_id AND u.attempt=e.attempt WHERE u.id=(SELECT id FROM agent_updates WHERE agent_id=? ORDER BY created_at DESC,rowid DESC LIMIT 1) AND e.disposition='abandon')`, agentID).Scan(&abandoned); err != nil {
			return AgentUpdateView{}, err
		}
		if !abandoned {
			return AgentUpdateView{}, errors.New("center: verify and dispose the failed execution before another update")
		}
	}
	update, err := s.queueAgentUpdateTx(ctx, tx, agentID, targetVersion, "Agent update to "+targetVersion+" queued")
	if err != nil {
		return AgentUpdateView{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentUpdateView{}, err
	}
	return update, nil
}

// QueueAgentUpdates starts every eligible online Agent independently so one
// unhealthy node cannot prevent the rest of the fleet from moving forward.
func (s *Store) QueueAgentUpdates(ctx context.Context, targetVersion string) ([]string, error) {
	targetVersion = strings.TrimPrefix(strings.TrimSpace(targetVersion), "v")
	if targetVersion == "" || !semver.IsValid("v"+targetVersion) {
		return nil, errors.New("center: Agent rollout target version is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT agent.id, agent.version
		FROM agents agent
		WHERE agent.status = 'active'
		  AND agent.credential_revoked_at = ''
		  AND agent.remote_update_supported = 1
		  AND agent.last_seen_at > ?
		  AND agent.version <> ?
		  AND COALESCE((SELECT previous.state FROM agent_updates previous
			WHERE previous.agent_id = agent.id
			ORDER BY previous.created_at DESC, previous.rowid DESC LIMIT 1), '') <> 'failed'
		  AND NOT EXISTS (
			SELECT 1 FROM agent_updates active_task
			WHERE active_task.agent_id = agent.id AND active_task.state IN ('pending', 'running', 'installing')
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM agent_updates update_task
			WHERE update_task.agent_id = agent.id AND update_task.target_version = ?
		  )
		ORDER BY agent.last_seen_at DESC, agent.name, agent.id`, s.now().UTC().Add(-agentConnectedMaxAge).Format(time.RFC3339Nano), targetVersion, targetVersion)
	if err != nil {
		return nil, fmt.Errorf("center: select Agent rollout targets: %w", err)
	}
	candidateIDs := []string{}
	for rows.Next() {
		var agentID, currentVersion string
		if err := rows.Scan(&agentID, &currentVersion); err != nil {
			rows.Close()
			return nil, err
		}
		currentSemver := "v" + strings.TrimPrefix(strings.TrimSpace(currentVersion), "v")
		if semver.IsValid(currentSemver) && semver.Compare("v"+targetVersion, currentSemver) <= 0 {
			continue
		}
		candidateIDs = append(candidateIDs, agentID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, agentID := range candidateIDs {
		if _, err := s.queueAgentUpdateTx(ctx, tx, agentID, targetVersion, "Agent update to "+targetVersion+" queued automatically after Center upgrade"); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return candidateIDs, nil
}

func (s *Store) AgentUpdateRolloutStatus(ctx context.Context, targetVersion string) (AgentUpdateRolloutStatus, error) {
	targetVersion = strings.TrimPrefix(strings.TrimSpace(targetVersion), "v")
	status := AgentUpdateRolloutStatus{TargetVersion: targetVersion}
	if targetVersion == "" || !semver.IsValid("v"+targetVersion) {
		return status, errors.New("center: Agent rollout target version is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT agent.version, agent.last_seen_at, agent.remote_update_supported,
		COALESCE((SELECT update_task.state FROM agent_updates update_task WHERE update_task.agent_id = agent.id ORDER BY update_task.created_at DESC, update_task.rowid DESC LIMIT 1), ''),
		COALESCE((SELECT update_task.target_version FROM agent_updates update_task WHERE update_task.agent_id = agent.id ORDER BY update_task.created_at DESC, update_task.rowid DESC LIMIT 1), ''),
		COALESCE((SELECT update_task.last_error FROM agent_updates update_task WHERE update_task.agent_id = agent.id ORDER BY update_task.created_at DESC, update_task.rowid DESC LIMIT 1), ''),
		COALESCE((SELECT update_task.updated_at FROM agent_updates update_task WHERE update_task.agent_id = agent.id ORDER BY update_task.created_at DESC, update_task.rowid DESC LIMIT 1), '')
		FROM agents agent
		WHERE agent.status = 'active' AND agent.credential_revoked_at = ''
		ORDER BY agent.name, agent.id`)
	if err != nil {
		return status, fmt.Errorf("center: inspect Agent rollout status: %w", err)
	}
	defer rows.Close()
	connectedAfter := s.now().UTC().Add(-agentConnectedMaxAge)
	for rows.Next() {
		var currentVersion, lastSeenAt, updateState, updateTarget, updateError, updateUpdatedAt string
		var supported bool
		if err := rows.Scan(&currentVersion, &lastSeenAt, &supported, &updateState, &updateTarget, &updateError, &updateUpdatedAt); err != nil {
			return status, err
		}
		currentSemver := "v" + strings.TrimPrefix(strings.TrimSpace(currentVersion), "v")
		activeUpdate := updateState == "pending" || updateState == "running" || updateState == "installing"
		if !activeUpdate && semver.IsValid(currentSemver) && semver.Compare(currentSemver, "v"+targetVersion) > 0 {
			continue
		}
		status.Total++
		if !activeUpdate && strings.TrimPrefix(strings.TrimSpace(currentVersion), "v") == targetVersion && !(updateTarget == targetVersion && updateState == "failed") {
			status.Updated++
			continue
		}
		seen, parseErr := time.Parse(time.RFC3339Nano, lastSeenAt)
		connected := parseErr == nil && seen.After(connectedAfter)
		// An offline Agent cannot keep the completed Center update busy. Keep
		// its durable task intact for inspection; connectivity is not permission
		// to repeat an interrupted execution.
		if activeUpdate {
			if !connected {
				status.Offline++
				continue
			}
			if updateTarget != targetVersion {
				status.Manual++
				continue
			}
			if updateState == "installing" && strings.TrimSpace(updateError) != "" {
				status.Failed++
				continue
			}
			progressAt, progressErr := time.Parse(time.RFC3339Nano, updateUpdatedAt)
			if progressErr != nil || !progressAt.After(s.now().UTC().Add(-agentUpdateProgressTimeout)) {
				status.Failed++
				continue
			}
			status.Updating++
			continue
		}
		if !connected {
			status.Offline++
			continue
		}
		if updateTarget == targetVersion && (updateState == "failed" || updateState == "succeeded") {
			status.Failed++
			continue
		}
		if updateState == "failed" {
			status.Manual++
			continue
		}
		if !supported {
			status.Manual++
			continue
		}
		if !connected {
			status.Offline++
			continue
		}
		status.Pending++
	}
	if err := rows.Err(); err != nil {
		return status, err
	}
	return status, nil
}

func (s *Store) queueAgentUpdateTx(ctx context.Context, tx *sql.Tx, agentID, targetVersion, eventMessage string) (AgentUpdateView, error) {
	token, err := randomToken(18)
	if err != nil {
		return AgentUpdateView{}, err
	}
	now := s.now().UTC()
	update := AgentUpdateView{ID: "agent-update-" + token, TargetVersion: targetVersion, State: "pending", UpdatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_updates(id, agent_id, target_version, state, created_at, updated_at) VALUES(?, ?, ?, 'pending', ?, ?)`, update.ID, agentID, targetVersion, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return AgentUpdateView{}, fmt.Errorf("center: queue Agent update: %w", err)
	}
	if err := s.recordTaskEvent(ctx, tx, update.ID, agentID, "agent.update", 1, "queued", eventMessage); err != nil {
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

func (s *Store) completeAgentUpdate(ctx context.Context, commit projectionCommit, agentID, taskID string, expectedAttempt int64, succeeded bool, taskError string, recoveryRequired bool) error {
	taskError = strings.TrimSpace(taskError)
	if len(taskError) > 1024 {
		taskError = taskError[:1024]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var targetVersion, currentState, previousError string
	var attempt int64
	if err := tx.QueryRowContext(ctx, `SELECT target_version, state, attempt, last_error FROM agent_updates WHERE id = ? AND agent_id = ?`, taskID, agentID).Scan(&targetVersion, &currentState, &attempt, &previousError); err != nil {
		return errors.New("center: Agent update task not found")
	}
	if attempt != expectedAttempt || expectedAttempt <= 0 {
		return errors.New("center: Agent update task is stale")
	}
	if recoveryRequired && (succeeded || taskError == "" || (currentState != "installing" && !(currentState == "failed" && previousError == taskError))) {
		return errInvalidReconciliationDisposition
	}
	desiredState := "succeeded"
	if !succeeded {
		desiredState = "failed"
		if taskError == "" {
			taskError = "Agent update failed"
		}
	}
	if currentState == desiredState && (!recoveryRequired || previousError == taskError) {
		return commit(tx)
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
	return commit(tx)
}
