package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
