package center

import (
	"context"
	"database/sql"
	"errors"
)

// Metadata only. The plan revision binds the stored input; the recovery receipt
// records cancellation without creating an execution or claiming it succeeded.
type AgentReinstallUnclaimedWork struct {
	TaskID   string `json:"taskId"`
	Kind     string `json:"kind"`
	Revision int64  `json:"revision"`
}

func reinstallUnclaimedTaskID(kind, id string, revision int64) string {
	switch kind {
	case "application.apply", "agent.update", "node.ip-quality", "node.network-quality", "node.return-route", "node.international-bandwidth", "node.host-profile", "meridian.link-bandwidth", "meridian.link-bandwidth-server", "xray.configuration.inspect", "xray.configuration.apply":
		return id
	case "agent.decommission":
		return agentDecommissionTaskID(id)
	}
	if revision > 0 {
		switch kind {
		case "node.listener.apply":
			return nodeListenerTaskID(id, revision)
		case "landing.server.apply":
			return landingServerTaskID(id, revision)
		case "landing.proxy.apply":
			return landingProxyTaskID(id, revision)
		}
	}
	return ""
}

func reinstallAbandonStatement(task AgentTask, agentID, now string) (string, []any, error) {
	switch task.Kind {
	case "agent.update":
		return `UPDATE agent_updates SET state='failed',last_error='Abandoned during reinstall recovery',lease_expires_at='',updated_at=? WHERE id=? AND agent_id=? AND attempt=? AND state IN ('failed','installing','running','pending')`, []any{now, task.ID, agentID, task.Attempt}, nil
	case "agent.decommission":
		return `UPDATE agent_decommissions SET state='abandoned',last_error='Abandoned during reinstall recovery',lease_expires_at='',callback_token_hash=X'',updated_at=? WHERE agent_id=? AND attempt=? AND state IN ('failed','cleaning','running','pending')`, []any{now, agentID, task.Attempt}, nil
	default:
		return executionAbandonStatement(task, agentID, now)
	}
}

func (s *Store) cancelReinstallUnclaimedWork(ctx context.Context, tx *sql.Tx, agentID string, work AgentReinstallUnclaimedWork, now string) error {
	// The enclosing transaction recomputed the full review and selected only
	// pending attempt-zero tasks. Recheck historical authority before mutation.
	var authorized bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND task_id=?) OR EXISTS(SELECT 1 FROM agent_reinstall_app_preparations WHERE deployment_id=? OR listener_task_id=?)`, agentID, work.TaskID, work.TaskID, work.TaskID).Scan(&authorized); err != nil {
		return err
	}
	if authorized {
		return errors.New("center: previous task attempt changed; inspect it before settlement")
	}
	task := AgentTask{ID: work.TaskID, Kind: work.Kind, Revision: work.Revision}
	query, args, err := reinstallAbandonStatement(task, agentID, now)
	if err != nil {
		return err
	}
	updated, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return errors.New("center: previous task attempt changed; inspect it before settlement")
	}
	if work.Kind == "application.apply" {
		_, err := tx.ExecContext(ctx, `UPDATE applications SET status='failed',updated_at=?
		 WHERE id=(SELECT application_id FROM deployments WHERE id=?)
		 AND (SELECT id FROM deployments WHERE application_id=applications.id ORDER BY created_at DESC,rowid DESC LIMIT 1)=?`, now, work.TaskID, work.TaskID)
		if err != nil {
			return err
		}
	}
	return s.recordTaskEvent(ctx, tx, work.TaskID, agentID, work.Kind, work.Revision, "failed", "Unissued task cancelled during reviewed reinstall recovery; saved intent retained")
}
