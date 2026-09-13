package center

import (
	"context"
	"database/sql"
	"time"
)

// A selected task and its one-use permission are one durable fact. Failure to
// seal or persist authorization rolls back the attempt and all selection writes.
func (s *Store) claimExecutionTask(ctx context.Context, agentID, credential, sessionID string, wait time.Duration) (*AgentTask, error) {
	if err := s.executionClaimAllowed(ctx, agentID, sessionID); err != nil {
		return nil, err
	}
	commit := func(tx *sql.Tx, task *AgentTask) error {
		if task != nil {
			auth, err := s.persistExecutionAuthorization(ctx, tx, agentID, sessionID, *task)
			if err != nil {
				return err
			}
			task.Authorization = auth
		}
		return tx.Commit()
	}
	return s.waitAndClaimTask(ctx, agentID, credential, wait, "", commit)
}
