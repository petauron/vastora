package center

import (
	"context"
	"database/sql"
	"strings"
)

// Retain obsolete preparation evidence without authorizing another proxy writer.
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
