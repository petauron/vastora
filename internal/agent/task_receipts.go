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

	"github.com/petauron/vastora/internal/platform"
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
		WHERE state IN ('processing', 'reconciliation_required', 'reconciliation_acknowledged') AND task_kind IN ('application.apply', 'legacy')
		ORDER BY created_at, task_id LIMIT 1`).Scan(&taskID, &taskKind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("agent: inspect unresolved application task receipts: %w", err)
	}
	return taskID, taskKind, nil
}

func (s *Store) PendingTaskCompletion(ctx context.Context) (*TaskCompletion, error) {
	var taskID string
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT task_id, sealed_completion FROM task_receipts
		WHERE state IN ('completed', 'reconciliation_required') ORDER BY updated_at, task_id LIMIT 1`).Scan(&taskID, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("agent: inspect pending task completions: %w", err)
	}
	plaintext, err := secret.Open(s.key, sealed, taskCompletionContext(taskID))
	if err != nil {
		return nil, errors.New("agent: pending task completion is invalid")
	}
	var completion TaskCompletion
	if json.Unmarshal(plaintext, &completion) != nil || completion.TaskID != taskID || completion.Attempt <= 0 {
		return nil, errors.New("agent: pending task completion is invalid")
	}
	return &completion, nil
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
	_, _ = s.db.ExecContext(ctx, `DELETE FROM task_receipts WHERE task_kind = 'agent.update' AND state = 'processing' AND updated_at < ?`, s.now().UTC().Add(-30*24*time.Hour).Format(time.RFC3339Nano))
	var attempt int64
	var executorRuntimeGeneration int
	var storedHash []byte
	var state string
	var sealed []byte
	err = s.db.QueryRowContext(ctx, `SELECT attempt, task_hash, state, COALESCE(sealed_completion, X''), runtime_generation FROM task_receipts WHERE task_id = ?`, task.ID).Scan(&attempt, &storedHash, &state, &sealed, &executorRuntimeGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		now := s.now().UTC().Format(time.RFC3339Nano)
		if task.Kind == "application.apply" {
			executorRuntimeGeneration = platform.ApplicationRuntimeGeneration
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO task_receipts(task_id, task_kind, runtime_generation, attempt, task_hash, state, created_at, updated_at) VALUES(?, ?, ?, ?, ?, 'processing', ?, ?)`, task.ID, task.Kind, executorRuntimeGeneration, task.Attempt, hash, now, now); err != nil {
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
	if state == "completed" || state == "acknowledged" || state == "reconciliation_required" || state == "reconciliation_acknowledged" {
		plaintext, err := secret.Open(s.key, sealed, taskCompletionContext(task.ID))
		if err != nil {
			return nil, errors.New("agent: stored task completion is invalid")
		}
		var completion TaskCompletion
		if json.Unmarshal(plaintext, &completion) != nil || completion.TaskID != task.ID {
			return nil, errors.New("agent: stored task completion is invalid")
		}
		if task.Kind == "application.apply" && completion.ApplicationRuntimeGeneration != executorRuntimeGeneration {
			return nil, errors.New("agent: stored task completion has invalid runtime generation evidence")
		}
		if (state == "reconciliation_required" || state == "reconciliation_acknowledged") && task.Reconcile {
			result, err := s.db.ExecContext(ctx, `UPDATE task_receipts SET attempt = ?, state = 'processing', sealed_completion = NULL, updated_at = ? WHERE task_id = ? AND state IN ('reconciliation_required', 'reconciliation_acknowledged')`, task.Attempt, s.now().UTC().Format(time.RFC3339Nano), task.ID)
			if err != nil {
				return nil, fmt.Errorf("agent: begin explicit task reconciliation: %w", err)
			}
			if changed, _ := result.RowsAffected(); changed != 1 {
				return nil, errors.New("agent: task reconciliation state changed")
			}
			return nil, nil
		}
		completion.Attempt = task.Attempt
		if state == "reconciliation_required" || state == "reconciliation_acknowledged" {
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
	if task.Kind == "agent.decommission" || task.Kind == "agent.update" {
		// The durable host helper makes this task resumable. Re-delivery after a
		// lease recovery must re-arm that helper instead of manufacturing an
		// unknown terminal outcome.
		if _, err := s.db.ExecContext(ctx, `UPDATE task_receipts SET attempt = ?, updated_at = ? WHERE task_id = ? AND state = 'processing'`, task.Attempt, s.now().UTC().Format(time.RFC3339Nano), task.ID); err != nil {
			return nil, fmt.Errorf("agent: resume host lifecycle receipt: %w", err)
		}
		return nil, nil
	}
	if resumable, err := s.resumableThreeXUIControllerPromotion(ctx, task); err != nil {
		return nil, err
	} else if resumable {
		return nil, nil
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
		completion.ApplicationRuntimeGeneration = executorRuntimeGeneration
	}
	if err := s.storeTaskCompletion(ctx, completion, hash); err != nil {
		return nil, err
	}
	return &completion, nil
}

func (s *Store) RecordTaskCompletion(ctx context.Context, completion TaskCompletion) error {
	var hash []byte
	var taskKind string
	var executorRuntimeGeneration int
	if err := s.db.QueryRowContext(ctx, `SELECT task_hash, task_kind, runtime_generation FROM task_receipts WHERE task_id = ? AND attempt <= ?`, completion.TaskID, completion.Attempt).Scan(&hash, &taskKind, &executorRuntimeGeneration); err != nil {
		return fmt.Errorf("agent: read task receipt for completion: %w", err)
	}
	if taskKind == "application.apply" && completion.ApplicationRuntimeGeneration != executorRuntimeGeneration {
		return errors.New("agent: task completion runtime generation does not match its executor receipt")
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE task_receipts SET state = CASE WHEN state = 'reconciliation_required' THEN 'reconciliation_acknowledged' ELSE 'acknowledged' END, updated_at = ? WHERE task_id = ? AND state IN ('completed', 'acknowledged', 'reconciliation_required', 'reconciliation_acknowledged')`, s.now().UTC().Format(time.RFC3339Nano), taskID); err != nil {
		return fmt.Errorf("agent: clear acknowledged task completion: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM three_x_ui_controller_promotions WHERE task_id = ? AND phase = 'applied'`, taskID); err != nil {
		return fmt.Errorf("agent: clear acknowledged 3x-ui controller promotion: %w", err)
	}
	return tx.Commit()
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
