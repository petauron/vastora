package center

import (
	"context"
	"database/sql"
)

// A reconnect grant starts a new machine identity, not another process on the
// previous machine. Preserve outcomes and sealed evidence for inspection, but
// never use that evidence to publish state on its replacement. Session history
// remains immutable so even the new credential cannot resurrect an old session.
func retireAgentExecutionIdentity(ctx context.Context, tx *sql.Tx, agentID, now string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_execution_sessions WHERE agent_id=?`, agentID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE task_executions SET
		identity_retired_at=?,updated_at=?,
		state=CASE WHEN state IN ('offered','running','helper_running') THEN 'unknown' ELSE state END,
		last_error=CASE WHEN state IN ('offered','running','helper_running') AND last_error='' THEN 'Agent identity replaced; inspect the previous machine execution' ELSE last_error END
		WHERE agent_id=? AND disposition='' AND state<>'succeeded' AND identity_retired_at=''`, now, now, agentID)
	return err
}
