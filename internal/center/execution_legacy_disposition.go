package center

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
)

// AbandonLegacyReceipt terminates a matching old attempt, if it still exists,
// and releases only its archive fence atomically. The archive is never changed: even
// an abandoned operation can contain the only copy of generated credentials.
func (s *Store) AbandonLegacyReceipt(ctx context.Context, id, adminID string, input controlplane.ExecutionDisposition) error {
	if input.Action != "abandon" || !input.ExecutionStopped || strings.TrimSpace(input.Note) == "" || len(input.Note) > 1024 {
		return errors.New("center: confirm stopped execution and record verification")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var allowed bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admins WHERE id=?)`, adminID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return errors.New("center: administrator authorization required")
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_executions WHERE id=? AND kind='legacy.receipt' AND state='unknown' AND disposition='')`, id).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return errExecutionAuthorization
	}
	receipt, agentID, err := s.readLegacyReceipt(ctx, tx, id)
	if err != nil {
		return err
	}
	task := AgentTask{ID: receipt.TaskID, Kind: receipt.Kind, Attempt: receipt.Attempt}
	var valid bool
	archiveOnly := false
	switch task.Kind {
	case "application.apply", "application.command":
		// Exact identity and attempt are checked by the business update below.
	case "landing.server.apply":
		task.Revision, valid = landingServerTaskRevision(task.ID)
		if !valid || task.ID != landingServerTaskID(agentID, task.Revision) {
			return errExecutionAuthorization
		}
	case "landing.proxy.apply":
		task.Revision, valid = landingProxyTaskRevision(task.ID)
		if !valid || task.ID != landingProxyTaskID(agentID, task.Revision) {
			return errExecutionAuthorization
		}
	case "gateway.routes.apply":
		task.Revision, valid = gatewayTaskRevision(task.ID)
		if !valid || task.ID != gatewayRouteTaskID(agentID, task.Revision) {
			return errExecutionAuthorization
		}
	case "gateway.component.apply":
		task.Revision, valid = gatewayComponentTaskGeneration(task.ID)
		if !valid || task.ID != gatewayComponentTaskID(agentID, task.Revision) {
			return errExecutionAuthorization
		}
	case "node.listener.apply":
		task.Revision, valid = nodeListenerTaskRevision(task.ID)
		if !valid || task.ID != nodeListenerTaskID(agentID, task.Revision) {
			return errExecutionAuthorization
		}
	case "tunnel.state.apply":
		task.Revision, valid = tunnelTaskRevision(task.ID)
		if !valid || task.ID != tunnelTaskID(agentID, task.Revision) {
			return errExecutionAuthorization
		}
	default:
		// Obsolete kinds have no executable compatibility path. The operator's
		// stopped-execution attestation retires only the preserved archive.
		archiveOnly = true
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if !archiveOnly {
		query, args, err := executionAbandonStatement(task, agentID, now)
		if err != nil {
			return err
		}
		if task.Kind == "application.apply" {
			query += ` AND NOT EXISTS(SELECT 1 FROM deployments newer WHERE newer.application_id=deployments.application_id AND newer.rowid>deployments.rowid)`
		}
		if task.Kind == "application.command" {
			query += ` AND NOT EXISTS(SELECT 1 FROM application_commands newer WHERE newer.application_id=application_commands.application_id AND newer.rowid>application_commands.rowid)`
		}
		updated, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		matched, err := updated.RowsAffected()
		if err != nil {
			return err
		}
		if task.Kind == "application.apply" && matched == 1 {
			if _, err := tx.ExecContext(ctx, `UPDATE applications SET status='failed',updated_at=? WHERE id=(SELECT application_id FROM deployments WHERE id=?)`, now, task.ID); err != nil {
				return err
			}
		}
	}
	updated, err := tx.ExecContext(ctx, `UPDATE task_executions SET disposition='abandon',disposition_note=?,disposition_actor=?,disposed_at=?,updated_at=? WHERE id=? AND kind='legacy.receipt' AND state='unknown' AND disposition=''`, controlplane.SafeError(input.Note), adminID, now, now, id)
	if err != nil {
		return err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return errExecutionAuthorization
	}
	return tx.Commit()
}
