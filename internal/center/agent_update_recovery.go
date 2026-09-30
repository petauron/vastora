package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/secret"
)

// Dispose only the exact failed attempt confirmed by the administrator. Keep
// its failure and sealed evidence unchanged; the caller queues a new task in
// this transaction, so any remaining execution fence rolls this back too.
func (s *Store) authorizeAgentUpdateRecovery(ctx context.Context, tx *sql.Tx, agentID, targetVersion string, input *AgentUpdateRecoveryInput) error {
	if !input.ExecutionStopped || strings.TrimSpace(input.Note) == "" || len(input.Note) > 1024 {
		return errors.New("center: confirm the exact failed update is stopped and record recovery verification")
	}
	var admin bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admins WHERE id=?)`, input.adminID).Scan(&admin); err != nil || !admin {
		return errors.New("center: administrator authorization required")
	}
	var id, state string
	var attempt int64
	if err := tx.QueryRowContext(ctx, `SELECT id,state,attempt FROM agent_updates WHERE agent_id=? ORDER BY created_at DESC,rowid DESC LIMIT 1`, agentID).Scan(&id, &state, &attempt); err != nil {
		return err
	}
	if id != input.FailedUpdateID || state != "failed" {
		return errors.New("center: failed update changed; verify its current state")
	}
	var executionID, executionState, disposition string
	var sealed []byte
	err := tx.QueryRowContext(ctx, `SELECT id,state,disposition,sealed_task FROM task_executions WHERE task_id=? AND agent_id=? AND kind='agent.update' AND attempt=?`, id, agentID, attempt).Scan(&executionID, &executionState, &disposition, &sealed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	note := controlplane.SafeError(strings.TrimSpace(input.Note))
	if err == nil {
		if executionState != "failed" || disposition != "" && disposition != "abandon" {
			return errors.New("center: failed update execution is not stopped")
		}
		raw, err := secret.Open(s.key, sealed, []byte("execution-task:"+executionID))
		if err != nil {
			return errors.New("center: execution evidence cannot be verified")
		}
		var task AgentTask
		if json.Unmarshal(raw, &task) != nil || task.ID != id || task.Kind != "agent.update" || task.Attempt != attempt {
			return errors.New("center: execution evidence does not match its identity")
		}
		if disposition == "" {
			updated, err := tx.ExecContext(ctx, `UPDATE task_executions SET disposition='abandon',disposition_note=?,disposition_actor=?,disposed_at=?,updated_at=? WHERE id=? AND state='failed' AND disposition=''`, note, input.adminID, now, now, executionID)
			if err != nil {
				return err
			}
			if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
				return errExecutionAuthorization
			}
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,json_object('actor',?,'note',?,'targetVersion',?,'recoveredAt',?))`, "agent-update-recovery:"+id, input.adminID, note, targetVersion, now)
	return err
}
