package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/secret"
)

// The journal is written before changing Docker's restart policy or Xray's
// settings. Healthy/allowed flags are intentionally not persistent: recovery
// must install a closed gate and obtain new link and business evidence.
type landingRuntimeState struct {
	Desired       landing.DesiredState  `json:"desired"`
	Applied       *landing.DesiredState `json:"applied,omitempty"`
	ApplicationID string                `json:"applicationId,omitempty"`
	ContainerID   string                `json:"containerId,omitempty"`
	Bridge        string                `json:"bridge,omitempty"`
	RestartPolicy string                `json:"restartPolicy,omitempty"`
	Route         *landing.RouteChange  `json:"route,omitempty"`
	Phase         string                `json:"phase"`
}

func (state landingRuntimeState) validate() error {
	if err := state.Desired.Validate(); err != nil {
		return err
	}
	if state.Applied != nil {
		if err := state.Applied.Validate(); err != nil || state.Applied.NodeID != state.Desired.NodeID || state.Applied.Revision > state.Desired.Revision {
			return errors.New("agent: invalid applied landing revision")
		}
	}
	switch state.Phase {
	case "prepared", "applied", "restoring":
	default:
		return errors.New("agent: invalid landing recovery phase")
	}
	if state.Route != nil {
		if state.ApplicationID == "" || state.ContainerID == "" || state.Bridge == "" || (state.RestartPolicy != "unless-stopped" && state.RestartPolicy != "always" && state.RestartPolicy != "no") {
			return errors.New("agent: incomplete landing cutover checkpoint")
		}
		if _, _, err := state.Route.NextWrite(state.Route.Before, true); err != nil {
			return errors.New("agent: invalid landing routing checkpoint")
		}
		owner := &state.Desired
		if state.Applied != nil && state.Applied.Revision == state.Route.Revision {
			owner = state.Applied
		}
		if owner.Revision != state.Route.Revision || owner.Proxy == nil || owner.Proxy.ApplicationID != state.ApplicationID {
			return errors.New("agent: landing checkpoint does not match its application revision")
		}
		if _, err := landing.NewBridgeGate(owner.Proxy.Peer, state.Bridge, state.Route.Revision); err != nil {
			return errors.New("agent: invalid landing checkpoint bridge identity")
		}
	}
	return nil
}

func (s *Store) landingRuntime(ctx context.Context) (*landingRuntimeState, error) {
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT sealed_state FROM landing_runtime_state WHERE id = 1`).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("agent: cannot read landing recovery state")
	}
	plain, err := secret.Open(s.key, sealed, []byte("agent-landing-runtime"))
	if err != nil {
		return nil, errors.New("agent: cannot decrypt landing recovery state")
	}
	var state landingRuntimeState
	if json.Unmarshal(plain, &state) != nil || state.validate() != nil {
		return nil, errors.New("agent: invalid landing recovery state")
	}
	connection, err := s.Connection(ctx)
	if err != nil || connection.AgentID != state.Desired.NodeID {
		return nil, errors.New("agent: landing recovery belongs to a different node")
	}
	return &state, nil
}

// Caller serializes all landing and selected application mutations. Matching
// revision retries may update the phase but cannot replace the desired plan.
func (s *Store) saveLandingRuntime(ctx context.Context, state landingRuntimeState) error {
	if err := state.validate(); err != nil {
		return err
	}
	connection, err := s.Connection(ctx)
	if err != nil || connection.AgentID != state.Desired.NodeID {
		return errors.New("agent: landing state is owned by another node")
	}
	var previous []byte
	readErr := s.db.QueryRowContext(ctx, `SELECT sealed_state FROM landing_runtime_state WHERE id = 1`).Scan(&previous)
	if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
		return errors.New("agent: cannot read landing recovery checkpoint")
	}
	current, err := s.landingRuntime(ctx)
	if err != nil {
		return err
	}
	if current != nil {
		if current.Desired.Revision > state.Desired.Revision {
			return errors.New("agent: stale landing revision")
		}
		if current.Desired.Revision == state.Desired.Revision {
			before, _ := json.Marshal(current.Desired)
			after, _ := json.Marshal(state.Desired)
			if string(before) != string(after) {
				return errors.New("agent: landing desired state changed without a new revision")
			}
		}
	}
	plain, err := json.Marshal(state)
	if err != nil {
		return errors.New("agent: cannot encode landing recovery state")
	}
	sealed, err := secret.Seal(s.key, plain, []byte("agent-landing-runtime"))
	if err != nil {
		return errors.New("agent: cannot encrypt landing recovery state")
	}
	var result sql.Result
	if errors.Is(readErr, sql.ErrNoRows) {
		result, err = s.db.ExecContext(ctx, `INSERT INTO landing_runtime_state(id, sealed_state) VALUES(1, ?)
			ON CONFLICT(id) DO NOTHING`, sealed)
	} else {
		result, err = s.db.ExecContext(ctx, `UPDATE landing_runtime_state SET sealed_state = ? WHERE id = 1 AND sealed_state = ?`, sealed, previous)
	}
	if err != nil {
		return errors.New("agent: cannot save landing recovery state")
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return errors.New("agent: landing recovery state changed during the operation")
	}
	return nil
}
