package center

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/petauron/vastora/internal/xrayrecovery"
)

const xrayConfigurationRecoverySchemaSQL = `CREATE TABLE xray_configuration_recoveries (
 agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
 id TEXT NOT NULL UNIQUE,
 action TEXT NOT NULL CHECK(action IN ('inspect','runtime','agent_state')),
 state TEXT NOT NULL CHECK(state IN ('pending','running','awaiting_decision','succeeded','failed')),
 attempt INTEGER NOT NULL DEFAULT 0,
 lease_expires_at TEXT NOT NULL DEFAULT '',
 expected_runtime_sha256 TEXT NOT NULL DEFAULT '',
 expected_agent_sha256 TEXT NOT NULL DEFAULT '',
 expected_agent_revision INTEGER NOT NULL DEFAULT 0,
 error TEXT NOT NULL DEFAULT '',
 result_json TEXT NOT NULL DEFAULT 'null' CHECK(json_valid(result_json)),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);`

type XrayConfigurationRecoveryView struct {
	AgentID       string               `json:"agentId"`
	ApplicationID string               `json:"applicationId"`
	ID            string               `json:"id"`
	Action        string               `json:"action"`
	State         string               `json:"state"`
	Error         string               `json:"error,omitempty"`
	Result        *xrayrecovery.Result `json:"result,omitempty"`
	UpdatedAt     string               `json:"updatedAt"`
}

func (s *Store) XrayConfigurationRecovery(ctx context.Context, agentID string) (XrayConfigurationRecoveryView, error) {
	var view XrayConfigurationRecoveryView
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT agent_id,application_id,id,action,state,error,result_json,updated_at FROM xray_configuration_recoveries WHERE agent_id=?`, agentID).Scan(&view.AgentID, &view.ApplicationID, &view.ID, &view.Action, &view.State, &view.Error, &raw, &view.UpdatedAt)
	if err != nil {
		return view, err
	}
	if raw != "null" {
		var result xrayrecovery.Result
		if json.Unmarshal([]byte(raw), &result) != nil {
			return view, errors.New("center: invalid stored Xray configuration recovery result")
		}
		view.Result = &result
	}
	return view, nil
}
