package center

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
)

// DisposeHelperExecution resolves a helper without issuing another command.
// Historical execution state/evidence is retained; disposition releases its fence.
func (s *Store) DisposeHelperExecution(ctx context.Context, id, adminID string, input controlplane.ExecutionDisposition) error {
	if (input.Action != "confirm-completed" && input.Action != "abandon") || !input.ExecutionStopped || strings.TrimSpace(input.Note) == "" || len(input.Note) > 1024 {
		return errors.New("center: confirm stopped execution and record verification")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var admin bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admins WHERE id=?)`, adminID).Scan(&admin); err != nil {
		return err
	}
	if !admin {
		return errors.New("center: administrator authorization required")
	}
	var taskID, agentID, kind string
	var attempt int64
	if err := tx.QueryRowContext(ctx, `SELECT task_id,agent_id,attempt,kind FROM task_executions WHERE id=? AND kind IN ('agent.update','agent.decommission') AND state IN ('failed','unknown') AND disposition=''`, id).Scan(&taskID, &agentID, &attempt, &kind); err != nil {
		return errExecutionAuthorization
	}
	state, message := "failed", "Abandoned by operator"
	if input.Action == "confirm-completed" {
		if kind == "agent.update" {
			var target, version, lastSeen string
			if err := tx.QueryRowContext(ctx, `SELECT u.target_version,a.version,a.last_seen_at FROM agent_updates u JOIN agents a ON a.id=u.agent_id WHERE u.id=? AND u.agent_id=? AND u.attempt=?`, taskID, agentID, attempt).Scan(&target, &version, &lastSeen); err != nil {
				return errExecutionAuthorization
			}
			seen, err := time.Parse(time.RFC3339Nano, lastSeen)
			if err != nil || version != target || !seen.After(s.now().Add(-agentConnectedMaxAge)) {
				return errors.New("center: target version must be observed online before confirming completion")
			}
		}
		// Decommission has no heartbeat after successful host removal. The
		// administrator's explicit confirmation and note attest actual cleanup;
		// offline status alone is never interpreted as completed removal.
		state, message = "succeeded", ""
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	query := `UPDATE agent_updates SET state=?,last_error=?,lease_expires_at='',updated_at=? WHERE id=? AND agent_id=? AND attempt=? AND state IN ('failed','installing','running')`
	args := []any{state, message, now, taskID, agentID, attempt}
	if kind == "agent.decommission" {
		query = `UPDATE agent_decommissions SET state=?,last_error=?,lease_expires_at='',callback_token_hash=X'',updated_at=? WHERE agent_id=? AND attempt=? AND state IN ('failed','cleaning','running')`
		args = []any{state, message, now, agentID, attempt}
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errExecutionAuthorization
	}
	result, err = tx.ExecContext(ctx, `UPDATE task_executions SET disposition=?,disposition_note=?,disposition_actor=?,disposed_at=?,updated_at=? WHERE id=? AND disposition='' AND state IN ('failed','unknown')`, input.Action, controlplane.SafeError(input.Note), adminID, now, now, id)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errExecutionAuthorization
	}
	return tx.Commit()
}
