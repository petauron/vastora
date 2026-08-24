package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type TaskReconciliationRetry struct {
	TaskID string `json:"taskId"`
	Kind   string `json:"kind"`
	Queued bool   `json:"queued"`
}

func (s *Store) RetryTaskReconciliation(ctx context.Context, taskID string) (TaskReconciliationRetry, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return TaskReconciliationRetry{}, errors.New("center: task is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskReconciliationRetry{}, err
	}
	defer tx.Rollback()

	now := s.now().UTC().Format(time.RFC3339Nano)
	if strings.HasPrefix(taskID, "application-command-") {
		var agentID, applicationID, appKey, state string
		var reconciliationRequired int
		err := tx.QueryRowContext(ctx, `SELECT command.agent_id, command.application_id, application.app_key, command.state, command.reconciliation_required
			FROM application_commands command JOIN applications application ON application.id = command.application_id
			WHERE command.id = ?`, taskID).Scan(&agentID, &applicationID, &appKey, &state, &reconciliationRequired)
		if errors.Is(err, sql.ErrNoRows) {
			return TaskReconciliationRetry{}, errors.New("center: task not found")
		}
		if err != nil {
			return TaskReconciliationRetry{}, err
		}
		if appKey != threeXUIAppKey || state != "failed" || reconciliationRequired != 1 {
			return TaskReconciliationRetry{}, errors.New("center: task does not require reconciliation")
		}
		result, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = 'pending', reconciliation_required = 0, lease_expires_at = '', error = '', updated_at = ? WHERE id = ? AND state = 'failed' AND reconciliation_required = 1`, now, taskID)
		if err != nil {
			return TaskReconciliationRetry{}, fmt.Errorf("center: retry application operation reconciliation: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return TaskReconciliationRetry{}, errors.New("center: task changed while retrying reconciliation")
		}
		if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", 1, "queued", "manual reconciliation retry queued"); err != nil {
			return TaskReconciliationRetry{}, err
		}
		if err := tx.Commit(); err != nil {
			return TaskReconciliationRetry{}, err
		}
		return TaskReconciliationRetry{TaskID: taskID, Kind: "application.command", Queued: true}, nil
	}

	var agentID, applicationID, appKey, state string
	var reconciliationRequired int
	err = tx.QueryRowContext(ctx, `SELECT agent_id, application_id, app_key, state, reconciliation_required FROM deployments WHERE id = ?`, taskID).Scan(&agentID, &applicationID, &appKey, &state, &reconciliationRequired)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskReconciliationRetry{}, errors.New("center: task not found")
	}
	if err != nil {
		return TaskReconciliationRetry{}, err
	}
	if appKey != threeXUIAppKey || state != "failed" || reconciliationRequired != 1 {
		return TaskReconciliationRetry{}, errors.New("center: task does not require reconciliation")
	}
	result, err := tx.ExecContext(ctx, `UPDATE deployments SET state = 'pending', reconciliation_required = 0, lease_expires_at = '', error = '', updated_at = ? WHERE id = ? AND state = 'failed' AND reconciliation_required = 1`, now, taskID)
	if err != nil {
		return TaskReconciliationRetry{}, fmt.Errorf("center: retry deployment reconciliation: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return TaskReconciliationRetry{}, errors.New("center: task changed while retrying reconciliation")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE applications SET status = 'pending', updated_at = ? WHERE id = ?`, now, applicationID); err != nil {
		return TaskReconciliationRetry{}, err
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.apply", applicationTaskRevision, "queued", "manual reconciliation retry queued"); err != nil {
		return TaskReconciliationRetry{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskReconciliationRetry{}, err
	}
	return TaskReconciliationRetry{TaskID: taskID, Kind: "application.apply", Queued: true}, nil
}
