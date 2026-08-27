package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	taskLeaseDuration  = 5 * time.Minute
	defaultActionLimit = 50
	maxActionLimit     = 100
)

type ActionView struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"taskId"`
	AgentID   string    `json:"agentId"`
	Kind      string    `json:"kind"`
	Revision  int64     `json:"revision"`
	Event     string    `json:"event"`
	Message   string    `json:"message,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Store) recordTaskEvent(ctx context.Context, tx *sql.Tx, taskID, agentID, kind string, revision int64, event, message string) error {
	id, err := randomToken(18)
	if err != nil {
		return err
	}
	message = strings.TrimSpace(message)
	if len(message) > 1024 {
		message = message[:1024]
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_events(id, task_id, agent_id, kind, revision, event, message, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, id, taskID, agentID, kind, revision, event, message, s.now().UTC().Format(time.RFC3339Nano))
	if err == nil {
		s.notifyTaskEventAfterCommit(id, taskID, agentID, kind, event)
	}
	return err
}

// notifyTaskEventAfterCommit waits until SQLite exposes the inserted event on
// the Store connection before waking any waiter. Store transactions own the
// single database connection, so the visibility check cannot pass before the
// transaction commits. Rolled-back events never produce a notification.
func (s *Store) notifyTaskEventAfterCommit(eventID, taskID, agentID, kind, event string) {
	s.startBackground(func() {
		var committed int
		if err := s.db.QueryRowContext(s.backgroundCtx, `SELECT 1 FROM task_events WHERE id = ?`, eventID).Scan(&committed); err != nil {
			return
		}
		s.taskChanges.notify("agent:" + agentID)
		s.taskChanges.notify("task:" + taskID)
		if kind == "application.command" && (event == "succeeded" || event == "failed") {
			s.taskChanges.notify(threeXUIInboundPlanResetWakeKey)
		}
	})
}

func (s *Store) recordStandaloneTaskEvent(ctx context.Context, taskID, agentID, kind string, revision int64, event, message string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, kind, revision, event, message); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) recoverExpiredTasks(ctx context.Context, agentID string) error {
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	type expiredTask struct {
		id       string
		kind     string
		revision int64
	}
	expired := []expiredTask{}
	queries := []struct {
		query string
		kind  string
	}{
		{`SELECT id, 1 FROM deployments WHERE agent_id = ? AND state = 'running' AND lease_expires_at <> '' AND lease_expires_at <= ?`, "application.apply"},
		{`SELECT id, 1 FROM application_commands WHERE agent_id = ? AND state = 'running' AND lease_expires_at <> '' AND lease_expires_at <= ?`, "application.command"},
		{`SELECT 'gateway-component-' || gateway_node_id || '-g' || generation, generation FROM gateway_components WHERE gateway_node_id = ? AND status = 'applying' AND lease_expires_at <> '' AND lease_expires_at <= ?`, "gateway.component.apply"},
		{`SELECT 'gateway-route-' || gateway_node_id || '-r' || desired_revision, desired_revision FROM gateway_states WHERE gateway_node_id = ? AND status = 'applying' AND lease_expires_at <> '' AND lease_expires_at <= ?`, "gateway.routes.apply"},
		{`SELECT 'tunnel-' || agent_id || '-r' || desired_revision, desired_revision FROM cloudflare_tunnels WHERE agent_id = ? AND status = 'applying' AND lease_expires_at <> '' AND lease_expires_at <= ?`, "tunnel.state.apply"},
		{`SELECT 'agent-decommission-' || agent_id, 1 FROM agent_decommissions WHERE agent_id = ? AND state = 'running' AND lease_expires_at <> '' AND lease_expires_at <= ?`, "agent.decommission"},
	}
	for _, candidate := range queries {
		rows, err := tx.QueryContext(ctx, candidate.query, agentID, now.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("center: inspect expired tasks: %w", err)
		}
		for rows.Next() {
			var task expiredTask
			task.kind = candidate.kind
			if err := rows.Scan(&task.id, &task.revision); err != nil {
				rows.Close()
				return err
			}
			expired = append(expired, task)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if len(expired) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE deployments SET state = 'pending', lease_expires_at = '', error = 'task lease expired; queued for retry', updated_at = ? WHERE agent_id = ? AND state = 'running' AND lease_expires_at <> '' AND lease_expires_at <= ?`, now.Format(time.RFC3339Nano), agentID, now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = 'pending', lease_expires_at = '', error = 'task lease expired; queued for retry', updated_at = ? WHERE agent_id = ? AND state = 'running' AND lease_expires_at <> '' AND lease_expires_at <= ?`, now.Format(time.RFC3339Nano), agentID, now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE applications SET status = 'pending', updated_at = ? WHERE id IN (
		SELECT application_id FROM deployments WHERE agent_id = ? AND state = 'pending' AND error = 'task lease expired; queued for retry'
	)`, now.Format(time.RFC3339Nano), agentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gateway_components SET status = 'failed', lease_expires_at = '', last_error = 'task lease expired; queued for retry', updated_at = ? WHERE gateway_node_id = ? AND status = 'applying' AND lease_expires_at <> '' AND lease_expires_at <= ?`, now.Format(time.RFC3339Nano), agentID, now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gateway_states SET status = 'failed', lease_expires_at = '', last_error = 'task lease expired; queued for retry', updated_at = ? WHERE gateway_node_id = ? AND status = 'applying' AND lease_expires_at <> '' AND lease_expires_at <= ?`, now.Format(time.RFC3339Nano), agentID, now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE cloudflare_tunnels SET status = 'failed', lease_expires_at = '', last_error = 'task lease expired; queued for retry', updated_at = ? WHERE agent_id = ? AND status = 'applying' AND lease_expires_at <> '' AND lease_expires_at <= ?`, now.Format(time.RFC3339Nano), agentID, now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_decommissions SET state = 'pending', lease_expires_at = '', last_error = 'task lease expired; queued for retry', updated_at = ? WHERE agent_id = ? AND state = 'running' AND lease_expires_at <> '' AND lease_expires_at <= ?`, now.Format(time.RFC3339Nano), agentID, now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	for _, task := range expired {
		if err := s.recordTaskEvent(ctx, tx, task.id, agentID, task.kind, task.revision, "lease_expired", "task lease expired; queued for retry"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListActions(ctx context.Context, limit int) ([]ActionView, error) {
	if limit <= 0 {
		limit = defaultActionLimit
	}
	if limit > maxActionLimit {
		limit = maxActionLimit
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, task_id, agent_id, kind, revision, event, message, created_at FROM task_events ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("center: list actions: %w", err)
	}
	defer rows.Close()
	values := []ActionView{}
	for rows.Next() {
		var value ActionView
		var createdAt string
		if err := rows.Scan(&value.ID, &value.TaskID, &value.AgentID, &value.Kind, &value.Revision, &value.Event, &value.Message, &createdAt); err != nil {
			return nil, err
		}
		value.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, errors.New("center: invalid action timestamp")
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
