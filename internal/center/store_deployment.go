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
	Installed            bool
	ID                   string
	Version              string
	PackageRevision      int
	Manifest             json.RawMessage
	RegistryCredentialID string
}

func (s *Store) activeDeployment(ctx context.Context, agentID, appKey string) (activeDeploymentState, error) {
	var operation string
	var state activeDeploymentState
	err := s.db.QueryRowContext(ctx, `SELECT d.id, d.operation, d.app_version, d.manifest_json, COALESCE(d.registry_credential_id, ''), d.package_revision FROM deployments d
		JOIN applications a ON a.id = d.application_id AND a.node_id = d.agent_id
		WHERE d.agent_id = ? AND a.app_key = ? AND d.state = 'succeeded'
		ORDER BY d.created_at DESC, d.rowid DESC LIMIT 1`, agentID, appKey).Scan(&state.ID, &operation, &state.Version, &state.Manifest, &state.RegistryCredentialID, &state.PackageRevision)
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
