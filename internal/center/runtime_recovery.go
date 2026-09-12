package center

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/petauron/vastora/internal/controlplane"
)

const runtimeRecoverySettingsPrefix = "agent_runtime_recovery:"

// Only negative intents can cross the startup fence outside a failed
// application's identity. Do not claim a positive task and reject it later:
// that would consume its lease and block the actual repair behind it.
func (s *Store) claimRecoveryStopTask(ctx context.Context, tx *sql.Tx, agentID, stage string) (*AgentTask, error) {
	var eligible bool
	switch stage {
	case "application", "landing":
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM landing_proxy_states WHERE node_id=? AND json_extract(desired_json,'$.proxy') IS NULL)`, agentID).Scan(&eligible); err != nil {
			return nil, err
		}
		if eligible {
			return s.claimLandingProxyTask(ctx, tx, agentID)
		}
	case "listener":
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM node_listener_states WHERE node_id=? AND json_array_length(desired_json,'$.listener.routes')=0)`, agentID).Scan(&eligible); err != nil {
			return nil, err
		}
		if eligible {
			return s.claimNodeListenerTask(ctx, tx, agentID)
		}
	}
	return nil, nil
}

func saveRuntimeRecoveryApplications(ctx context.Context, tx *sql.Tx, agentID string, values []controlplane.RecoveryApplication) error {
	key := runtimeRecoverySettingsPrefix + agentID
	if len(values) == 0 {
		_, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key=?`, key)
		return err
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, string(encoded))
	return err
}
