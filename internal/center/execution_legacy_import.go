package center

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/secret"
)

// ImportLegacyReceipt archives evidence, not executable intent. Repeated
// identical uploads acknowledge the committed archive; different content never
// overwrites it, including after an administrator has disposed the record.
func (s *Store) ImportLegacyReceipt(ctx context.Context, agentID, credential string, input controlplane.LegacyReceiptImport) (string, error) {
	if err := s.authenticateAgent(ctx, agentID, credential); err != nil {
		return "", err
	}
	raw, digest, err := controlplane.EncodeLegacyReceipt(input.Receipt)
	if err != nil || digest != input.Digest {
		return "", errors.New("center: invalid legacy evidence digest")
	}
	id := controlplane.LegacyReceiptArchiveID(agentID, input.Receipt.TaskID, input.Receipt.Attempt)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var activeSession string
	if err := tx.QueryRowContext(ctx, `SELECT session_id FROM agent_execution_sessions WHERE agent_id=?`, agentID).Scan(&activeSession); err != nil || input.SessionID == "" || input.SessionID != activeSession {
		return "", errExecutionAuthorization
	}
	var existingDigest string
	var existingSealed []byte
	err = tx.QueryRowContext(ctx, `SELECT digest,sealed_task FROM task_executions WHERE id=? AND agent_id=? AND kind='legacy.receipt'`, id, agentID).Scan(&existingDigest, &existingSealed)
	if err == nil {
		previous, openErr := secret.Open(s.key, existingSealed, []byte("execution-task:"+id))
		if existingDigest != digest || openErr != nil || !bytes.Equal(previous, raw) {
			return "", errors.New("center: legacy evidence conflicts with retained archive")
		}
		return id, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	sealed, err := secret.Seal(s.key, raw, []byte("execution-task:"+id))
	if err != nil {
		return "", err
	}
	state, message := "unknown", "Legacy execution requires operator verification"
	if input.Receipt.State == "acknowledged" {
		state, message = "succeeded", ""
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,last_error,expires_at,created_at,updated_at)
	 SELECT ?,?,?,'legacy.receipt',?,?,?,?,?,?,?,?,?,?
	 WHERE NOT EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND state IN ('offered','running','helper_running') AND disposition='')`, id, agentID, id, input.Receipt.Attempt, input.SessionID, digest, sealed, state, "legacy:"+input.Receipt.State, message, now, now, now, agentID)
	if err != nil {
		return "", err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return "", errExecutionBlocked
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}
