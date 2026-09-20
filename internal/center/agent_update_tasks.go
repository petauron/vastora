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
	Blocked       int    `json:"blocked"`
}

// This bounds the busy indicator, not the durable installation or its backup.
// A helper that already owns a migration must remain recoverable after the UI
// reports that it needs attention.
const agentUpdateProgressTimeout = 5 * time.Minute

// A strictly newer running version supersedes an old update failure, but does
// not rewrite its evidence or authorize replay of any unresolved execution.
func agentUpdateFailureSuperseded(currentVersion, failedTarget string) bool {
	current := "v" + strings.TrimPrefix(strings.TrimSpace(currentVersion), "v")
	failed := "v" + strings.TrimPrefix(strings.TrimSpace(failedTarget), "v")
	return semver.IsValid(current) && semver.IsValid(failed) && semver.Compare(current, failed) > 0
}

func agentVersionBehindTarget(currentVersion, targetVersion string) bool {
	current := "v" + strings.TrimPrefix(strings.TrimSpace(currentVersion), "v")
	target := "v" + strings.TrimPrefix(strings.TrimSpace(targetVersion), "v")
	return semver.IsValid(current) && semver.IsValid(target) && semver.Compare(current, target) < 0
}

func isAgentUpdateTaskID(value string) bool {
	return strings.HasPrefix(value, "agent-update-")
}

type agentUpdateExecutionQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Terminal business-task evidence remains available for explicit recovery, but
// it does not make restarting the Agent unsafe. Active work and unresolved host
// lifecycle helpers still fence the update. An older Agent update is also safe
// once a newer running version proves that attempt was superseded.
func unresolvedExecutionBlocksAgentUpdate(ctx context.Context, queryer agentUpdateExecutionQueryer, agentID, currentVersion string) (bool, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT execution.kind,execution.state,COALESCE(update_task.target_version,'')
		FROM task_executions execution
		LEFT JOIN agent_updates update_task ON execution.kind='agent.update' AND update_task.id=execution.task_id AND update_task.agent_id=execution.agent_id
		WHERE execution.agent_id=? AND execution.disposition='' AND execution.state<>'succeeded'`, agentID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, state, targetVersion string
		if err := rows.Scan(&kind, &state, &targetVersion); err != nil {
			return false, err
		}
		if state == "offered" || state == "running" || state == "helper_running" || kind == "agent.decommission" {
			return true, nil
		}
		if kind == "agent.update" && !agentUpdateFailureSuperseded(currentVersion, targetVersion) {
			return true, nil
		}
	}
	return false, rows.Err()
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
	var runtimeRecovery bool
	if err := tx.QueryRowContext(ctx, `SELECT runtime_recovery<>'' FROM agents WHERE id=?`, agentID).Scan(&runtimeRecovery); err != nil {
		return AgentUpdateView{}, err
	}
	executionBlocked, err := unresolvedExecutionBlocksAgentUpdate(ctx, tx, agentID, currentVersion)
	if err != nil {
		return AgentUpdateView{}, err
	}
	if runtimeRecovery || executionBlocked {
		return AgentUpdateView{}, errors.New("center: resolve outstanding execution and runtime recovery before updating")
	}
	if paused, err := executionClaimsPaused(ctx, tx); err != nil {
		return AgentUpdateView{}, err
	} else if paused {
		return AgentUpdateView{}, errExecutionBlocked
	}
	var lastState, lastID, lastTarget string
	err = tx.QueryRowContext(ctx, `SELECT state,id,target_version FROM agent_updates WHERE agent_id=? ORDER BY created_at DESC,rowid DESC LIMIT 1`, agentID).Scan(&lastState, &lastID, &lastTarget)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AgentUpdateView{}, err
	}
	if recovery != nil {
		if lastState != "failed" || recovery.FailedUpdateID != lastID || !recovery.ExecutionStopped || strings.TrimSpace(recovery.Note) == "" || len(recovery.Note) > 1024 {
			return AgentUpdateView{}, errors.New("center: confirm the exact failed update is stopped and record recovery verification")
		}
		var admin bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admins WHERE id=?)`, recovery.adminID).Scan(&admin); err != nil || !admin {
			return AgentUpdateView{}, errors.New("center: administrator authorization required")
		}
		// Keep the original failure immutable. Recovery authorizes a new task,
		// never replay or a fabricated success for the old attempt.
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,json_object('actor',?,'note',?,'targetVersion',?,'recoveredAt',?))`, "agent-update-recovery:"+lastID, recovery.adminID, recovery.Note, targetVersion, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
			return AgentUpdateView{}, err
		}
	}
	if lastState == "failed" && recovery == nil && !agentUpdateFailureSuperseded(currentVersion, lastTarget) {
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
	if paused, err := executionClaimsPaused(ctx, s.db); err != nil {
		return nil, err
	} else if paused {
		return []string{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT agent.id, agent.version, COALESCE(previous.state,''), COALESCE(previous.target_version,'')
		FROM agents agent
		LEFT JOIN agent_updates previous ON previous.id=(SELECT id FROM agent_updates WHERE agent_id=agent.id ORDER BY created_at DESC,rowid DESC LIMIT 1)
		WHERE agent.status = 'active'
		  AND agent.credential_revoked_at = ''
		  AND agent.remote_update_supported = 1
		  AND agent.last_seen_at > ?
		  AND agent.version <> ?
		  AND agent.runtime_recovery = ''
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
	type rolloutCandidate struct {
		id, currentVersion string
	}
	candidates := []rolloutCandidate{}
	for rows.Next() {
		var agentID, currentVersion, previousState, previousTarget string
		if err := rows.Scan(&agentID, &currentVersion, &previousState, &previousTarget); err != nil {
			rows.Close()
			return nil, err
		}
		if previousState == "failed" && !agentUpdateFailureSuperseded(currentVersion, previousTarget) {
			continue
		}
		currentSemver := "v" + strings.TrimPrefix(strings.TrimSpace(currentVersion), "v")
		if semver.IsValid(currentSemver) && semver.Compare("v"+targetVersion, currentSemver) <= 0 {
			continue
		}
		candidates = append(candidates, rolloutCandidate{id: agentID, currentVersion: currentVersion})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	queuedIDs := make([]string, 0, len(candidates))
	queueErrors := make([]error, 0)
	for _, candidate := range candidates {
		blocked, err := unresolvedExecutionBlocksAgentUpdate(ctx, s.db, candidate.id, candidate.currentVersion)
		if err != nil {
			queueErrors = append(queueErrors, fmt.Errorf("center: inspect Agent %s before independent update: %w", candidate.id, err))
			continue
		}
		if blocked {
			continue
		}
		if _, err := s.QueueAgentUpdate(ctx, candidate.id, targetVersion); err != nil {
			queueErrors = append(queueErrors, fmt.Errorf("center: queue Agent %s independently: %w", candidate.id, err))
			continue
		}
		queuedIDs = append(queuedIDs, candidate.id)
	}
	return queuedIDs, errors.Join(queueErrors...)
}

func (s *Store) AgentUpdateRolloutStatus(ctx context.Context, targetVersion string) (AgentUpdateRolloutStatus, error) {
	targetVersion = strings.TrimPrefix(strings.TrimSpace(targetVersion), "v")
	status := AgentUpdateRolloutStatus{TargetVersion: targetVersion}
	if targetVersion == "" || !semver.IsValid("v"+targetVersion) {
		return status, errors.New("center: Agent rollout target version is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT agent.id,agent.version, agent.last_seen_at, agent.remote_update_supported,
		COALESCE((SELECT update_task.state FROM agent_updates update_task WHERE update_task.agent_id = agent.id ORDER BY update_task.created_at DESC, update_task.rowid DESC LIMIT 1), ''),
		COALESCE((SELECT update_task.target_version FROM agent_updates update_task WHERE update_task.agent_id = agent.id ORDER BY update_task.created_at DESC, update_task.rowid DESC LIMIT 1), ''),
		COALESCE((SELECT update_task.last_error FROM agent_updates update_task WHERE update_task.agent_id = agent.id ORDER BY update_task.created_at DESC, update_task.rowid DESC LIMIT 1), ''),
		COALESCE((SELECT update_task.updated_at FROM agent_updates update_task WHERE update_task.agent_id = agent.id ORDER BY update_task.created_at DESC, update_task.rowid DESC LIMIT 1), ''),
		agent.runtime_recovery<>''
		FROM agents agent
		WHERE agent.status = 'active' AND agent.credential_revoked_at = ''
		ORDER BY agent.name, agent.id`)
	if err != nil {
		return status, fmt.Errorf("center: inspect Agent rollout status: %w", err)
	}
	type rolloutAgent struct {
		id, currentVersion, lastSeenAt, updateState, updateTarget, updateError, updateUpdatedAt string
		supported, runtimeRecovery                                                              bool
	}
	agents := []rolloutAgent{}
	for rows.Next() {
		var agent rolloutAgent
		if err := rows.Scan(&agent.id, &agent.currentVersion, &agent.lastSeenAt, &agent.supported, &agent.updateState, &agent.updateTarget, &agent.updateError, &agent.updateUpdatedAt, &agent.runtimeRecovery); err != nil {
			rows.Close()
			return status, err
		}
		agents = append(agents, agent)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return status, err
	}
	if err := rows.Close(); err != nil {
		return status, err
	}
	connectedAfter := s.now().UTC().Add(-agentConnectedMaxAge)
	for _, agent := range agents {
		executionBlocked, err := unresolvedExecutionBlocksAgentUpdate(ctx, s.db, agent.id, agent.currentVersion)
		if err != nil {
			return status, err
		}
		blocked := agent.runtimeRecovery || executionBlocked
		currentSemver := "v" + strings.TrimPrefix(strings.TrimSpace(agent.currentVersion), "v")
		activeUpdate := agent.updateState == "pending" || agent.updateState == "running" || agent.updateState == "installing"
		if !activeUpdate && semver.IsValid(currentSemver) && semver.Compare(currentSemver, "v"+targetVersion) > 0 {
			continue
		}
		status.Total++
		if !activeUpdate && strings.TrimPrefix(strings.TrimSpace(agent.currentVersion), "v") == targetVersion && !(agent.updateTarget == targetVersion && agent.updateState == "failed") {
			status.Updated++
			continue
		}
		seen, parseErr := time.Parse(time.RFC3339Nano, agent.lastSeenAt)
		connected := parseErr == nil && seen.After(connectedAfter)
		// An offline Agent cannot keep the completed Center update busy. Keep
		// its durable task intact for inspection; connectivity is not permission
		// to repeat an interrupted execution.
		if activeUpdate {
			if !connected {
				status.Offline++
				continue
			}
			if agent.updateTarget != targetVersion {
				status.Blocked++
				continue
			}
			if agent.updateState == "installing" && strings.TrimSpace(agent.updateError) != "" {
				status.Failed++
				continue
			}
			progressAt, progressErr := time.Parse(time.RFC3339Nano, agent.updateUpdatedAt)
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
		if agent.updateTarget == targetVersion && (agent.updateState == "failed" || agent.updateState == "succeeded") {
			status.Failed++
			continue
		}
		if blocked || (agent.updateState == "failed" && !agentUpdateFailureSuperseded(agent.currentVersion, agent.updateTarget)) {
			status.Blocked++
			continue
		}
		if !agent.supported {
			status.Manual++
			continue
		}
		if !connected {
			status.Offline++
			continue
		}
		status.Pending++
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
