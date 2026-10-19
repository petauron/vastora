package center

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Only the retired proxy preparation table and its reserved task namespaces
// are fenced here. Ordinary installations and monitor restoration keep their
// own current authorization checks and are not interpreted as proxy work.
func rejectRetiredApplicationPreparation(ctx context.Context, tx *sql.Tx, agentID, taskID string) error {
	var retired bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_reinstall_app_preparations p JOIN agent_reinstall_operations op ON op.id=p.operation_id WHERE op.agent_id=? AND (p.deployment_id=? OR p.runtime_command_id=? OR p.listener_task_id=?))`, agentID, taskID, taskID, taskID).Scan(&retired); err != nil {
		return err
	}
	if retired || strings.HasPrefix(taskID, "reinstall-package-") || strings.HasPrefix(taskID, "reinstall-runtime-") {
		return errExecutionBlocked
	}
	return nil
}

func (s *Store) claimAgentReinstallTask(ctx context.Context, agentID, credential, requiredTaskID string, commitTask func(*sql.Tx, *AgentTask) error) (*AgentTask, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if paused, err := executionClaimsPaused(ctx, tx); err != nil {
		return nil, err
	} else if paused {
		return nil, errExecutionBlocked
	}
	var generation int
	var version string
	err = tx.QueryRowContext(ctx, `SELECT runtime_generation,version FROM agents WHERE id=? AND status='active' AND credential_revoked_at='' AND credential_hash=?`, agentID, tokenHash(credential)).Scan(&generation, &version)
	if err != nil {
		return nil, errExecutionAuthorization
	}
	if agentVersionBehindTarget(version, Version) {
		return nil, errExecutionBlocked
	}
	if blocked, err := unresolvedExecutionBlocksAgentWork(ctx, tx, agentID, version); err != nil {
		return nil, err
	} else if blocked {
		return nil, errExecutionBlocked
	}
	id, err := pendingAgentReinstallTask(ctx, tx, agentID, requiredTaskID)
	if err != nil {
		return nil, err
	}
	pending, err := readPendingApplicationDeployment(ctx, tx, agentID, id)
	if err != nil {
		return nil, err
	}
	return s.claimApplicationDeployment(ctx, tx, agentID, generation, pending, commitTask)
}

func pendingAgentReinstallTask(ctx context.Context, tx *sql.Tx, agentID, requiredTaskID string) (string, error) {
	var id string
	// Proxy package/runtime/listener restoration is owned by Glivela. Only
	// the existing independent monitor recovery remains in this channel.
	err := tx.QueryRowContext(ctx, `SELECT d.id FROM agent_reinstall_monitor_restorations m JOIN deployments d ON d.id=m.deployment_id JOIN agent_reinstall_monitor_rotations r ON r.command_id=m.rotation_command_id JOIN agent_reinstall_operations op ON op.id=r.operation_id
 WHERE d.agent_id=? AND op.agent_id=d.agent_id AND op.state='review_required' AND d.state='pending' AND d.attempt=0
 AND (?='' OR d.id=?) ORDER BY d.created_at,d.id LIMIT 1`, agentID, requiredTaskID, requiredTaskID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errExecutionBlocked
	}
	return id, err
}
