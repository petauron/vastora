package center

import (
	"context"
	"github.com/petauron/vastora/internal/controlplane"
)

// Build standalone execution fixtures for protocol unit tests. Production
// authorization is only persisted with its business claim in one transaction.
func (s *Store) PersistExecutionAuthorization(ctx context.Context, agentID, sessionID string, task AgentTask) (controlplane.ExecutionAuthorization, error) {
	if err := s.executionClaimAllowed(ctx, agentID, sessionID); err != nil {
		return controlplane.ExecutionAuthorization{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return controlplane.ExecutionAuthorization{}, err
	}
	defer tx.Rollback()
	auth, err := s.persistExecutionAuthorization(ctx, tx, agentID, sessionID, task)
	if err != nil {
		return controlplane.ExecutionAuthorization{}, err
	}
	if err := tx.Commit(); err != nil {
		return controlplane.ExecutionAuthorization{}, err
	}
	return auth, nil
}
