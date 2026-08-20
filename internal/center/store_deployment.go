package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

func (s *Store) HasActiveDeployment(ctx context.Context, agentID, appKey string) (bool, error) {
	active, err := s.activeDeployment(ctx, agentID, appKey)
	return active.Installed, err
}

type activeDeploymentState struct {
	Installed bool
	ID        string
	Version   string
	Manifest  json.RawMessage
}

func (s *Store) activeDeployment(ctx context.Context, agentID, appKey string) (activeDeploymentState, error) {
	var operation string
	var state activeDeploymentState
	err := s.db.QueryRowContext(ctx, `SELECT id, operation, app_version, manifest_json FROM deployments
		WHERE agent_id = ? AND app_key = ? AND state = 'succeeded'
		ORDER BY created_at DESC, rowid DESC LIMIT 1`, agentID, appKey).Scan(&state.ID, &operation, &state.Version, &state.Manifest)
	if errors.Is(err, sql.ErrNoRows) {
		return activeDeploymentState{}, nil
	}
	if err != nil {
		return activeDeploymentState{}, fmt.Errorf("center: read active deployment: %w", err)
	}
	state.Installed = operation == "install" || operation == "upgrade" || operation == "configure"
	if !state.Installed {
		state.Version = ""
		state.Manifest = nil
	}
	return state, nil
}
