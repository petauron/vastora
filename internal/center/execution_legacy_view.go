package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/secret"
)

// LegacyReceiptView deliberately excludes completion contents, which may hold
// generated credentials. Read one bounded archive only when explicitly opened.
type LegacyReceiptView struct {
	ExecutionID       string `json:"executionId"`
	TaskID            string `json:"taskId"`
	Kind              string `json:"kind"`
	Attempt           int64  `json:"attempt"`
	RuntimeGeneration int    `json:"runtimeGeneration"`
	State             string `json:"state"`
	HasCompletion     bool   `json:"hasCompletion"`
	CreatedAt         string `json:"createdAt"`
	UpdatedAt         string `json:"updatedAt"`
}

func (s *Store) InspectLegacyReceipt(ctx context.Context, id string) (LegacyReceiptView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LegacyReceiptView{}, err
	}
	defer tx.Rollback()
	receipt, _, err := s.readLegacyReceipt(ctx, tx, id)
	if err != nil {
		return LegacyReceiptView{}, err
	}
	return LegacyReceiptView{
		ExecutionID: id, TaskID: receipt.TaskID, Kind: receipt.Kind,
		Attempt: receipt.Attempt, RuntimeGeneration: receipt.RuntimeGeneration,
		State: receipt.State, HasCompletion: len(receipt.Completion) > 0,
		CreatedAt: receipt.CreatedAt, UpdatedAt: receipt.UpdatedAt,
	}, nil
}

func (s *Store) readLegacyReceipt(ctx context.Context, tx *sql.Tx, id string) (controlplane.LegacyReceipt, string, error) {
	var sealed []byte
	var digest, agentID string
	var size int
	if err := tx.QueryRowContext(ctx, `SELECT length(sealed_task) FROM task_executions WHERE id=? AND kind='legacy.receipt'`, id).Scan(&size); err != nil {
		return controlplane.LegacyReceipt{}, "", err
	}
	if size > controlplane.LegacyReceiptMaxPayloadBytes+1024 {
		return controlplane.LegacyReceipt{}, "", errors.New("center: legacy evidence exceeds size limit")
	}
	if err := tx.QueryRowContext(ctx, `SELECT sealed_task,digest,agent_id FROM task_executions WHERE id=? AND kind='legacy.receipt'`, id).Scan(&sealed, &digest, &agentID); err != nil {
		return controlplane.LegacyReceipt{}, "", err
	}
	raw, err := secret.Open(s.key, sealed, []byte("execution-task:"+id))
	if err != nil {
		return controlplane.LegacyReceipt{}, "", errors.New("center: legacy evidence integrity check failed")
	}
	var receipt controlplane.LegacyReceipt
	if json.Unmarshal(raw, &receipt) != nil {
		return controlplane.LegacyReceipt{}, "", errors.New("center: invalid legacy evidence")
	}
	_, actual, err := controlplane.EncodeLegacyReceipt(receipt)
	if err != nil || actual != digest || controlplane.LegacyReceiptArchiveID(agentID, receipt.TaskID, receipt.Attempt) != id {
		return controlplane.LegacyReceipt{}, "", errors.New("center: legacy evidence identity or digest mismatch")
	}
	return receipt, agentID, nil
}
