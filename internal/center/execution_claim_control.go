package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const executionClaimControlKey = "execution_claim_control"

type ExecutionClaimControl struct {
	Paused    bool   `json:"paused"`
	Actor     string `json:"actor"`
	UpdatedAt string `json:"updatedAt"`
}

func executionClaimsPaused(ctx context.Context, queryer networkQueryer) (bool, error) {
	var paused bool
	// Unknown or malformed persisted controls fail closed, not back to enabled.
	err := queryer.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM settings WHERE key='execution_claim_control' AND (NOT json_valid(value) OR COALESCE(json_type(value,'$.paused'),'')<>'false'))`).Scan(&paused)
	return paused, err
}

// A migration-owned pause protects ordinary work while a newer Center queues
// the Agent rollout needed for the cutover. It must not prevent that rollout
// from being created, otherwise the safety switch deadlocks its own recovery.
// An administrator-owned pause remains a true emergency stop for every task.
func agentUpdateRolloutPaused(ctx context.Context, queryer networkQueryer) (bool, error) {
	var raw string
	err := queryer.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, executionClaimControlKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	var control ExecutionClaimControl
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &control) != nil || json.Unmarshal([]byte(raw), &fields) != nil || (string(fields["paused"]) != "true" && string(fields["paused"]) != "false") {
		return true, errors.New("center: invalid execution claim control")
	}
	return control.Paused && !strings.HasPrefix(strings.TrimSpace(control.Actor), "migration:"), nil
}

// releaseMigrationExecutionPause completes the one-time protocol cutover after
// all eligible Agent updates have been queued. A concurrent administrator
// change wins; this compare-and-update never clears a newer emergency pause.
func (s *Store) releaseMigrationExecutionPause(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, executionClaimControlKey).Scan(&raw); errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	} else if err != nil {
		return err
	}
	var control ExecutionClaimControl
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &control) != nil || json.Unmarshal([]byte(raw), &fields) != nil || (string(fields["paused"]) != "true" && string(fields["paused"]) != "false") {
		return errors.New("center: invalid execution claim control")
	}
	if !control.Paused || !strings.HasPrefix(strings.TrimSpace(control.Actor), "migration:") {
		return tx.Commit()
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	updated, err := json.Marshal(ExecutionClaimControl{Paused: false, Actor: "system:agent-rollout", UpdatedAt: now})
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE settings SET value=? WHERE key=? AND value=?`, string(updated), executionClaimControlKey, raw)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 1 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO execution_claim_control_events(paused,actor,created_at) VALUES(0,'system:agent-rollout',?)`, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ExecutionClaimControl(ctx context.Context) (ExecutionClaimControl, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, executionClaimControlKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecutionClaimControl{}, nil
	}
	if err != nil {
		return ExecutionClaimControl{}, err
	}
	var value ExecutionClaimControl
	if json.Unmarshal([]byte(raw), &value) != nil {
		return ExecutionClaimControl{}, errors.New("center: invalid execution claim control")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil || (string(fields["paused"]) != "true" && string(fields["paused"]) != "false") {
		return ExecutionClaimControl{}, errors.New("center: invalid execution claim control")
	}
	return value, nil
}

func (s *Store) SetExecutionClaimControl(ctx context.Context, adminID string, paused bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var admin bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admins WHERE id=?)`, adminID).Scan(&admin); err != nil {
		return err
	}
	if !admin {
		return errors.New("center: administrator authorization required")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	value, err := json.Marshal(ExecutionClaimControl{Paused: paused, Actor: adminID, UpdatedAt: now})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, executionClaimControlKey, string(value)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO execution_claim_control_events(paused,actor,created_at) VALUES(?,?,?)`, paused, adminID, now); err != nil {
		return err
	}
	return tx.Commit()
}
