package center

import (
	"context"
	"crypto/subtle"
	"fmt"
	"time"
)

// AuthorizeDecommissionStep consumes one ordered permission over the public
// task-bound callback, which remains reachable after private-network removal.
// It does not renew the helper lifetime or replay a lost permission response.
func (s *Store) AuthorizeDecommissionStep(ctx context.Context, taskID, token string, attempt, sequence int64, phase string) error {
	if token == "" || attempt <= 0 || sequence <= 0 || sequence > 1024 {
		return errExecutionAuthorization
	}
	switch phase {
	case "command", "runtime", "files", "cancel-update", "done":
	default:
		return errExecutionAuthorization
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var agentID string
	var tokenDigest []byte
	if err := tx.QueryRowContext(ctx, `SELECT agent_id,callback_token_hash FROM agent_decommissions WHERE 'agent-decommission-'||agent_id=? AND attempt=? AND state='cleaning'`, taskID, attempt).Scan(&agentID, &tokenDigest); err != nil || subtle.ConstantTimeCompare(tokenDigest, tokenHash(token)) != 1 {
		return errExecutionAuthorization
	}
	previous := fmt.Sprintf("cleanup:%d:%%", sequence-1)
	initial := ""
	if sequence == 1 {
		initial = "cleanup"
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE task_executions SET phase=?,updated_at=? WHERE agent_id=? AND task_id=? AND attempt=? AND kind='agent.decommission' AND state='helper_running' AND disposition='' AND expires_at>? AND (phase=? OR phase LIKE ?) AND phase NOT LIKE 'cleanup:%:done'`, fmt.Sprintf("cleanup:%d:%s", sequence, phase), now, agentID, taskID, attempt, now, initial, previous)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errExecutionAuthorization
	}
	return tx.Commit()
}
