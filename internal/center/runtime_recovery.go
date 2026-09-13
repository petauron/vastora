package center

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/petauron/vastora/internal/controlplane"
)

const runtimeRecoverySettingsPrefix = "agent_runtime_recovery:"

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
