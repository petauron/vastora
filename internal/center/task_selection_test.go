package center

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Selection-only helpers for business projection tests. Production claims always
// commit the execution authorization with the selected business attempt.
func (s *Store) ClaimNextTask(ctx context.Context, agentID, credential string, requiredTaskIDs ...string) (*AgentTask, error) {
	requiredID := ""
	if len(requiredTaskIDs) != 0 {
		requiredID = strings.TrimSpace(requiredTaskIDs[0])
	}
	return s.claimNextTask(ctx, agentID, credential, requiredID, commitSelectionForTest)
}

func (s *Store) WaitAndClaimNextTask(ctx context.Context, agentID, credential string, wait time.Duration, requiredTaskIDs ...string) (*AgentTask, error) {
	requiredID := ""
	if len(requiredTaskIDs) != 0 {
		requiredID = strings.TrimSpace(requiredTaskIDs[0])
	}
	return s.waitAndClaimTask(ctx, agentID, credential, wait, requiredID, commitSelectionForTest)
}

func commitSelectionForTest(tx *sql.Tx, _ *AgentTask) error { return tx.Commit() }
