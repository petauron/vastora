package center

import (
	"context"
	"fmt"
	"time"
)

func (s *Store) UpdateHelperObserved(ctx context.Context, agentID, sessionID, executionID string) (bool, error) {
	var version, target, lastSeen string
	var supported bool
	now := s.now().UTC()
	err := s.db.QueryRowContext(ctx, `SELECT a.version,u.target_version,a.last_seen_at,a.remote_update_supported FROM task_executions e JOIN agent_updates u ON u.id=e.task_id AND u.agent_id=e.agent_id AND u.attempt=e.attempt JOIN agents a ON a.id=e.agent_id WHERE e.id=? AND e.agent_id=? AND e.session_id=? AND e.kind='agent.update' AND e.state='helper_running' AND e.phase='start' AND e.disposition='' AND e.expires_at>? AND u.state='installing'`, executionID, agentID, sessionID, now.Format(time.RFC3339Nano)).Scan(&version, &target, &lastSeen, &supported)
	if err != nil {
		return false, errExecutionAuthorization
	}
	seen, err := time.Parse(time.RFC3339Nano, lastSeen)
	return err == nil && supported && version == target && seen.After(now.Add(-agentConnectedMaxAge)), nil
}

// Each helper step consumes the preceding phase once. Lost replies cannot be
// interpreted as permission to repeat a stop, replacement or service start.
func (s *Store) CheckUpdateHelperStep(ctx context.Context, agentID, sessionID, executionID, phase string) error {
	previous, ok := map[string]string{"stop": "helper", "backup": "stop", "preserve": "backup", "install": "preserve", "start": "install"}[phase]
	if !ok {
		return errExecutionAuthorization
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE task_executions SET phase=?,updated_at=? WHERE id=? AND agent_id=? AND session_id=? AND kind='agent.update' AND state='helper_running' AND phase=? AND disposition='' AND expires_at>?`, phase, now, executionID, agentID, sessionID, previous, now)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errExecutionAuthorization
	}
	return nil
}

// beginAgentUpdateExecution transfers the already-started execution exactly
// once. Consuming the grant and transferring the business lease are atomic.
func (s *Store) beginAgentUpdateExecution(ctx context.Context, agentID, credential, taskID string, attempt int64, executionID, sessionID string) error {
	if err := s.authenticateAgent(ctx, agentID, credential); err != nil {
		return err
	}
	if executionID == "" || sessionID == "" || attempt <= 0 {
		return errExecutionAuthorization
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE task_executions SET state='helper_running',phase='helper',updated_at=?,expires_at=?
	 WHERE id=? AND agent_id=? AND task_id=? AND attempt=? AND session_id=? AND kind='agent.update'
	 AND state='running' AND phase='handoff' AND disposition='' AND expires_at>?
	 AND EXISTS(SELECT 1 FROM agent_execution_sessions WHERE agent_id=? AND session_id=?)`, stamp, now.Add(30*time.Minute).Format(time.RFC3339Nano), executionID, agentID, taskID, attempt, sessionID, stamp, agentID, sessionID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errExecutionAuthorization
	}
	result, err = tx.ExecContext(ctx, `UPDATE agent_updates SET state='installing',lease_expires_at='',updated_at=?
	 WHERE id=? AND agent_id=? AND attempt=? AND state='running' AND lease_expires_at>?`, stamp, taskID, agentID, attempt, stamp)
	if err != nil {
		return fmt.Errorf("center: transfer update lease: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errStaleTaskLease
	}
	return tx.Commit()
}
