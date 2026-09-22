package center

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type supersededLandingExecution struct {
	id               string
	agentID          string
	kind             string
	taskID           string
	executionAttempt int64
	desiredRevision  int64
	currentAttempt   int64
}

// A failed landing execution is terminal evidence. Once a newer desired
// revision or attempt exists, that old failure can no longer be recovered into
// the current business record. Keep the evidence, but release its execution
// fence so the current revision can run. Unknown and active executions remain
// fenced because their executor outcome has not been proven terminal.
func disposeSupersededLandingExecutionFailures(ctx context.Context, tx *sql.Tx, agentID, now string) (int64, error) {
	query := `SELECT execution.id,execution.agent_id,execution.kind,execution.task_id,execution.attempt,state.desired_revision,state.attempt
		FROM task_executions execution
		JOIN landing_proxy_states state ON execution.kind='landing.proxy.apply' AND state.node_id=execution.agent_id
		WHERE execution.disposition='' AND execution.state='failed' AND execution.phase='reported'
		UNION ALL
		SELECT execution.id,execution.agent_id,execution.kind,execution.task_id,execution.attempt,state.desired_revision,state.attempt
		FROM task_executions execution
		JOIN landing_server_states state ON execution.kind='landing.server.apply' AND state.node_id=execution.agent_id
		WHERE execution.disposition='' AND execution.state='failed' AND execution.phase='reported'`
	args := []any{}
	if agentID != "" {
		query = `SELECT * FROM (` + query + `) WHERE agent_id=?`
		args = append(args, agentID)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	candidates := []supersededLandingExecution{}
	for rows.Next() {
		var candidate supersededLandingExecution
		if err := rows.Scan(&candidate.id, &candidate.agentID, &candidate.kind, &candidate.taskID, &candidate.executionAttempt, &candidate.desiredRevision, &candidate.currentAttempt); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	var disposed int64
	for _, candidate := range candidates {
		var revision int64
		var valid bool
		switch candidate.kind {
		case "landing.proxy.apply":
			revision, valid = landingProxyTaskRevision(candidate.taskID)
			valid = valid && candidate.taskID == landingProxyTaskID(candidate.agentID, revision)
		case "landing.server.apply":
			revision, valid = landingServerTaskRevision(candidate.taskID)
			valid = valid && candidate.taskID == landingServerTaskID(candidate.agentID, revision)
		}
		if !valid || revision > candidate.desiredRevision || revision == candidate.desiredRevision && candidate.executionAttempt >= candidate.currentAttempt {
			continue
		}
		note := fmt.Sprintf("Superseded by landing revision %d attempt %d", candidate.desiredRevision, candidate.currentAttempt)
		result, err := tx.ExecContext(ctx, `UPDATE task_executions
			SET disposition='superseded',disposition_note=?,disposition_actor='system',disposed_at=?,updated_at=?
			WHERE id=? AND disposition='' AND state='failed'`, note, now, now, candidate.id)
		if err != nil {
			return 0, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if changed != 1 {
			return 0, fmt.Errorf("center: superseded landing execution %s changed during recovery", candidate.id)
		}
		disposed += changed
	}
	return disposed, nil
}

func (s *Store) recoverSupersededLandingExecutionFailures(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := disposeSupersededLandingExecutionFailures(ctx, tx, "", s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}
