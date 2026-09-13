package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/secret"
)

func legacyTaskCompletionContext(taskID string) []byte {
	return []byte("agent-task-completion:" + taskID)
}

// NextLegacyReceipt reads at most one bounded record in a consistent snapshot.
// It deliberately does not acknowledge, prune or replay anything. The caller
// must persist the returned evidence in Center before retiring local history.
func (s *Store) NextLegacyReceipt(ctx context.Context, afterID string) (*controlplane.LegacyReceipt, string, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	return s.readLegacyReceipt(ctx, tx, afterID)
}

func (s *Store) readLegacyReceipt(ctx context.Context, tx *sql.Tx, afterID string) (*controlplane.LegacyReceipt, string, error) {
	var item controlplane.LegacyReceipt
	var length int64
	err := tx.QueryRowContext(ctx, `SELECT task_id,task_kind,attempt,runtime_generation,task_hash,state,created_at,updated_at,COALESCE(length(sealed_completion),0) FROM task_receipts WHERE task_id>? ORDER BY task_id LIMIT 1`, afterID).Scan(&item.TaskID, &item.Kind, &item.Attempt, &item.RuntimeGeneration, &item.TaskHash, &item.State, &item.CreatedAt, &item.UpdatedAt, &length)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	invalid := errors.New("agent: legacy receipt evidence is invalid; retain local state for manual review")
	if item.TaskID == "" || item.Kind == "" || item.Attempt <= 0 || len(item.TaskHash) != sha256.Size || item.RuntimeGeneration < 0 {
		return nil, "", invalid
	}
	for _, stamp := range []string{item.CreatedAt, item.UpdatedAt} {
		if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
			return nil, "", invalid
		}
	}
	// Check the encrypted length before loading a potentially huge blob. AEAD
	// framing gets a small allowance; plaintext is checked independently below.
	if length > controlplane.LegacyReceiptMaxCompletionBytes+1024 {
		return nil, "", invalid
	}
	if length > 0 {
		var sealed []byte
		if err := tx.QueryRowContext(ctx, `SELECT sealed_completion FROM task_receipts WHERE task_id=?`, item.TaskID).Scan(&sealed); err != nil {
			return nil, "", err
		}
		raw, err := secret.Open(s.key, sealed, legacyTaskCompletionContext(item.TaskID))
		if err != nil || len(raw) > controlplane.LegacyReceiptMaxCompletionBytes {
			return nil, "", invalid
		}
		var identity struct {
			TaskID  string `json:"taskId"`
			Attempt int64  `json:"attempt"`
		}
		if json.Unmarshal(raw, &identity) != nil || identity.TaskID != item.TaskID || identity.Attempt != item.Attempt {
			return nil, "", invalid
		}
		// Preserve the complete original JSON, including fields from the old
		// runtime that are not represented by the current Go result type.
		item.Completion = append(json.RawMessage(nil), raw...)
	} else if item.State == "completed" || item.State == "reconciliation_required" || item.State == "reconciliation_acknowledged" {
		return nil, "", invalid
	}
	_, digest, err := controlplane.EncodeLegacyReceipt(item)
	if err != nil {
		return nil, "", err
	}
	return &item, digest, nil
}

// RetireLegacyReceipt is called only after matching Center archival confirmation.
// Re-read in the deleting transaction: concurrent changes or a lost confirmation
// never remove a different version of the original local evidence.
func (s *Store) RetireLegacyReceipt(ctx context.Context, taskID, digest string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	item, current, err := s.readLegacyReceipt(ctx, tx, "")
	if err != nil {
		return err
	}
	if item == nil || item.TaskID != taskID || current != digest {
		return errors.New("agent: legacy receipt changed before archival retirement")
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM task_receipts WHERE task_id=?`, taskID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("agent: legacy receipt was not retired")
	}
	return tx.Commit()
}
