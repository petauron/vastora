package agent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/secret"
)

type TaskCompletion struct {
	TaskID                       string                `json:"taskId"`
	Attempt                      int64                 `json:"attempt"`
	Result                       ApplicationTaskResult `json:"result"`
	Error                        string                `json:"error"`
	ReconciliationRequired       bool                  `json:"reconciliationRequired"`
	ApplicationRuntimeGeneration int                   `json:"applicationRuntimeGeneration"`
}

func (s *Store) UnresolvedApplicationTaskReceipt(ctx context.Context) (string, string, error) {
	var taskID, taskKind string
	err := s.db.QueryRowContext(ctx, `SELECT task_id, task_kind FROM task_receipts
		WHERE state IN ('processing', 'reconciliation_required') AND task_kind IN ('application.apply', 'legacy')
		ORDER BY created_at, task_id LIMIT 1`).Scan(&taskID, &taskKind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("agent: inspect unresolved application task receipts: %w", err)
	}
	return taskID, taskKind, nil
}

func (s *Store) HasProcessingTaskReceipts(ctx context.Context) (bool, error) {
	taskID, _, err := s.UnresolvedApplicationTaskReceipt(ctx)
	return taskID != "", err
}

// PrepareTaskReceipt writes durable intent before an external effect begins.
// A completed receipt is an outbox entry: duplicate delivery reuses its result
// instead of repeating the effect, including after an Agent restart.
func (s *Store) PrepareTaskReceipt(ctx context.Context, task DeploymentTask) (*TaskCompletion, error) {
	if strings.TrimSpace(task.ID) == "" || task.Attempt <= 0 {
		return nil, errors.New("agent: invalid task identity")
	}
	hash, err := deploymentTaskHash(task)
	if err != nil {
		return nil, err
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM task_receipts WHERE state = 'acknowledged' AND updated_at < ?`, s.now().UTC().Add(-30*24*time.Hour).Format(time.RFC3339Nano))
	var attempt int64
	var storedHash []byte
	var state string
	var sealed []byte
	err = s.db.QueryRowContext(ctx, `SELECT attempt, task_hash, state, COALESCE(sealed_completion, X'') FROM task_receipts WHERE task_id = ?`, task.ID).Scan(&attempt, &storedHash, &state, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		now := s.now().UTC().Format(time.RFC3339Nano)
		if _, err := s.db.ExecContext(ctx, `INSERT INTO task_receipts(task_id, task_kind, attempt, task_hash, state, created_at, updated_at) VALUES(?, ?, ?, ?, 'processing', ?, ?)`, task.ID, task.Kind, task.Attempt, hash, now, now); err != nil {
			return nil, fmt.Errorf("agent: record task receipt: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("agent: read task receipt: %w", err)
	}
	if subtle.ConstantTimeCompare(storedHash, hash) != 1 {
		return nil, errors.New("agent: duplicate task ID has different content")
	}
	if task.Attempt < attempt {
		return nil, errors.New("agent: stale duplicate task attempt")
	}
	if state == "completed" || state == "acknowledged" || state == "reconciliation_required" {
		plaintext, err := secret.Open(s.key, sealed, taskCompletionContext(task.ID))
		if err != nil {
			return nil, errors.New("agent: stored task completion is invalid")
		}
		var completion TaskCompletion
		if json.Unmarshal(plaintext, &completion) != nil || completion.TaskID != task.ID {
			return nil, errors.New("agent: stored task completion is invalid")
		}
		if state == "reconciliation_required" && task.Reconcile {
			result, err := s.db.ExecContext(ctx, `UPDATE task_receipts SET attempt = ?, state = 'processing', sealed_completion = NULL, updated_at = ? WHERE task_id = ? AND state = 'reconciliation_required'`, task.Attempt, s.now().UTC().Format(time.RFC3339Nano), task.ID)
			if err != nil {
				return nil, fmt.Errorf("agent: begin explicit task reconciliation: %w", err)
			}
			if changed, _ := result.RowsAffected(); changed != 1 {
				return nil, errors.New("agent: task reconciliation state changed")
			}
			return nil, nil
		}
		completion.Attempt = task.Attempt
		if state == "reconciliation_required" {
			return &completion, nil
		}
		if task.Attempt != attempt {
			if err := s.storeTaskCompletion(ctx, completion, hash); err != nil {
				return nil, err
			}
		}
		return &completion, nil
	}
	if state != "processing" {
		return nil, errors.New("agent: stored task receipt state is invalid")
	}
	// An existing processing receipt means the previous process may have
	// committed the external effect before it could persist its completion.
	// Fail closed instead of executing the effect a second time. The operator
	// can inspect the external system and issue a fresh, explicitly reconciled
	// command if needed.
	completion := TaskCompletion{
		TaskID:                 task.ID,
		Attempt:                task.Attempt,
		Error:                  "agent: previous task outcome is unknown; operator reconciliation is required",
		ReconciliationRequired: task.Kind == "application.apply",
	}
	if task.Kind == "application.apply" {
		completion.ApplicationRuntimeGeneration = task.RequiredRuntimeGeneration
	}
	if err := s.storeTaskCompletion(ctx, completion, hash); err != nil {
		return nil, err
	}
	return &completion, nil
}

func (s *Store) RecordTaskCompletion(ctx context.Context, completion TaskCompletion) error {
	var hash []byte
	if err := s.db.QueryRowContext(ctx, `SELECT task_hash FROM task_receipts WHERE task_id = ? AND attempt <= ?`, completion.TaskID, completion.Attempt).Scan(&hash); err != nil {
		return fmt.Errorf("agent: read task receipt for completion: %w", err)
	}
	return s.storeTaskCompletion(ctx, completion, hash)
}

func (s *Store) storeTaskCompletion(ctx context.Context, completion TaskCompletion, hash []byte) error {
	encoded, err := json.Marshal(completion)
	if err != nil {
		return fmt.Errorf("agent: encode task completion: %w", err)
	}
	sealed, err := secret.Seal(s.key, encoded, taskCompletionContext(completion.TaskID))
	if err != nil {
		return fmt.Errorf("agent: encrypt task completion: %w", err)
	}
	state := "completed"
	if completion.ReconciliationRequired {
		state = "reconciliation_required"
	}
	result, err := s.db.ExecContext(ctx, `UPDATE task_receipts SET attempt = ?, task_hash = ?, state = ?, sealed_completion = ?, updated_at = ? WHERE task_id = ?`, completion.Attempt, hash, state, sealed, s.now().UTC().Format(time.RFC3339Nano), completion.TaskID)
	if err != nil {
		return fmt.Errorf("agent: persist task completion: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("agent: task receipt disappeared before completion")
	}
	return nil
}

func (s *Store) AcknowledgeTaskCompletion(ctx context.Context, taskID string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE task_receipts SET state = 'acknowledged', updated_at = ? WHERE task_id = ? AND state IN ('completed', 'acknowledged')`, s.now().UTC().Format(time.RFC3339Nano), taskID); err != nil {
		return fmt.Errorf("agent: clear acknowledged task completion: %w", err)
	}
	return nil
}

func deploymentTaskHash(task DeploymentTask) ([]byte, error) {
	task.Attempt = 0
	task.Reconcile = false
	encoded, err := json.Marshal(task)
	if err != nil {
		return nil, fmt.Errorf("agent: hash task: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return digest[:], nil
}

func taskCompletionContext(taskID string) []byte {
	return []byte("agent-task-completion:" + taskID)
}
