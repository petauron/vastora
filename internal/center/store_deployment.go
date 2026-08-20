package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (s *Store) HasActiveDeployment(ctx context.Context, agentID, appKey string) (bool, error) {
	var operation string
	err := s.db.QueryRowContext(ctx, `SELECT operation FROM deployments
		WHERE agent_id = ? AND app_key = ? AND state = 'succeeded'
		ORDER BY created_at DESC, rowid DESC LIMIT 1`, agentID, appKey).Scan(&operation)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("center: read active deployment: %w", err)
	}
	return operation == "install" || operation == "upgrade", nil
}
