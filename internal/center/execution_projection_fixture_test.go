package center

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Business projection unit tests deliberately isolate the projection from the
// execution protocol. Production always supplies an authorization finalizer.
func commitProjectionOnlyForTest(tx *sql.Tx) error { return tx.Commit() }

func (s *Store) CompleteTask(ctx context.Context, agentID, credential, taskID string, expectedAttempt int64, succeeded bool, taskError string, rawResult json.RawMessage, executedRuntimeGeneration int) error {
	return s.completeTaskWithDisposition(ctx, commitProjectionOnlyForTest, agentID, credential, taskID, expectedAttempt, succeeded, taskError, rawResult, false, executedRuntimeGeneration)
}

func (s *Store) FinalizeExecution(ctx context.Context, agentID, sessionID, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.finalizeExecution(ctx, tx, agentID, sessionID, id); err != nil {
		return err
	}
	return tx.Commit()
}
