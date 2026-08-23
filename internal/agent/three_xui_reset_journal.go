package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type threeXUIResetJournal struct {
	OperationKey        string
	ServiceID           string
	ExpectedNextResetAt string
	PlanRevision        int64
	TargetInboundID     int
	TargetInboundTag    string
	SyncUsedBytes       int64
	DesiredEnabled      bool
	Status              string
	LastError           string
}

func threeXUIResetOperationKey(serviceID, expectedNextResetAt string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(serviceID) + "\x00" + strings.TrimSpace(expectedNextResetAt)))
	return "3xui-inbound-reset-" + hex.EncodeToString(digest[:18])
}

func (s *Store) beginThreeXUIReset(ctx context.Context, operationKey, serviceID, boundary string, revision int64, inboundID int, inboundTag string, observedUsedBytes int64, desiredEnabled bool) (threeXUIResetJournal, bool, error) {
	operationKey = strings.TrimSpace(operationKey)
	serviceID = strings.TrimSpace(serviceID)
	boundary = strings.TrimSpace(boundary)
	inboundTag = strings.TrimSpace(inboundTag)
	if operationKey == "" || operationKey != threeXUIResetOperationKey(serviceID, boundary) || serviceID == "" || boundary == "" || revision < 1 || inboundID < 1 || inboundTag == "" || observedUsedBytes < 0 {
		return threeXUIResetJournal{}, false, errors.New("agent: invalid REALITY inbound reset journal identity")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return threeXUIResetJournal{}, false, err
	}
	defer tx.Rollback()
	journal, err := readThreeXUIResetJournal(ctx, tx, operationKey)
	if err == nil {
		if journal.ServiceID != serviceID || journal.ExpectedNextResetAt != boundary || journal.PlanRevision != revision || journal.TargetInboundID != inboundID || journal.TargetInboundTag != inboundTag {
			return threeXUIResetJournal{}, false, errors.New("agent: REALITY inbound reset journal identity changed")
		}
		return journal, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return threeXUIResetJournal{}, false, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO three_x_ui_reset_journal(
		operation_key, service_id, expected_next_reset_at, plan_revision, target_inbound_id,
		target_inbound_tag, sync_used_bytes, desired_enabled, status, last_error, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, 'disable_started', '', ?, ?)`, operationKey, serviceID, boundary, revision, inboundID, inboundTag, observedUsedBytes, desiredEnabled, now, now)
	if err != nil {
		return threeXUIResetJournal{}, false, fmt.Errorf("agent: start REALITY inbound reset journal: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return threeXUIResetJournal{}, false, err
	}
	return threeXUIResetJournal{
		OperationKey: operationKey, ServiceID: serviceID, ExpectedNextResetAt: boundary,
		PlanRevision: revision, TargetInboundID: inboundID, TargetInboundTag: inboundTag,
		SyncUsedBytes: observedUsedBytes, DesiredEnabled: desiredEnabled, Status: "disable_started",
	}, true, nil
}

func readThreeXUIResetJournal(ctx context.Context, tx *sql.Tx, operationKey string) (threeXUIResetJournal, error) {
	return scanThreeXUIResetJournal(tx.QueryRowContext(ctx, `SELECT operation_key, service_id, expected_next_reset_at, plan_revision, target_inbound_id, target_inbound_tag, sync_used_bytes, desired_enabled, status, last_error
		FROM three_x_ui_reset_journal WHERE operation_key = ?`, operationKey))
}

type threeXUIResetJournalScanner interface {
	Scan(...any) error
}

func scanThreeXUIResetJournal(row threeXUIResetJournalScanner) (threeXUIResetJournal, error) {
	var journal threeXUIResetJournal
	var desiredEnabled int
	err := row.Scan(&journal.OperationKey, &journal.ServiceID, &journal.ExpectedNextResetAt, &journal.PlanRevision, &journal.TargetInboundID, &journal.TargetInboundTag, &journal.SyncUsedBytes, &desiredEnabled, &journal.Status, &journal.LastError)
	journal.DesiredEnabled = desiredEnabled != 0
	return journal, err
}

func (s *Store) completedThreeXUIResetJournal(ctx context.Context, operationKey, serviceID, boundary string, revision int64, centralInboundID int, inboundTag string, nodeID int) (threeXUIResetJournal, bool, error) {
	journal, err := scanThreeXUIResetJournal(s.db.QueryRowContext(ctx, `SELECT operation_key, service_id, expected_next_reset_at, plan_revision, target_inbound_id, target_inbound_tag, sync_used_bytes, desired_enabled, status, last_error
		FROM three_x_ui_reset_journal WHERE operation_key = ?`, operationKey))
	if errors.Is(err, sql.ErrNoRows) {
		return threeXUIResetJournal{}, false, nil
	}
	if err != nil {
		return threeXUIResetJournal{}, false, err
	}
	if journal.Status != "completed" {
		return journal, false, nil
	}
	if journal.ServiceID != serviceID || journal.ExpectedNextResetAt != boundary || journal.PlanRevision != revision || normalizedThreeXUIInboundTag(journal.TargetInboundTag, nodeID) != normalizedThreeXUIInboundTag(inboundTag, nodeID) || (nodeID == 0 && journal.TargetInboundID != centralInboundID) {
		return threeXUIResetJournal{}, false, errors.New("agent: completed REALITY inbound reset journal identity changed")
	}
	return journal, true, nil
}

func (s *Store) unfinishedThreeXUIResetJournal(ctx context.Context, serviceID string) (threeXUIResetJournal, bool, error) {
	journal, err := scanThreeXUIResetJournal(s.db.QueryRowContext(ctx, `SELECT operation_key, service_id, expected_next_reset_at, plan_revision, target_inbound_id, target_inbound_tag, sync_used_bytes, desired_enabled, status, last_error
		FROM three_x_ui_reset_journal WHERE service_id = ? AND status NOT IN ('completed', 'cancelled', 'cancelled_applied') ORDER BY updated_at DESC LIMIT 1`, strings.TrimSpace(serviceID)))
	if errors.Is(err, sql.ErrNoRows) {
		return threeXUIResetJournal{}, false, nil
	}
	if err != nil {
		return threeXUIResetJournal{}, false, err
	}
	return journal, true, nil
}

func (s *Store) markThreeXUIResetRecovery(ctx context.Context, operationKey, status, lastError string) error {
	if status != "retry" && status != "retry_applied" && status != "restore_pending" && status != "restore_pending_applied" && status != "cancelled" && status != "cancelled_applied" {
		return errors.New("agent: invalid REALITY inbound reset recovery state")
	}
	lastError = strings.TrimSpace(lastError)
	if len(lastError) > 1024 {
		lastError = lastError[:1024]
	}
	result, err := s.db.ExecContext(ctx, `UPDATE three_x_ui_reset_journal SET status = ?, last_error = ?, updated_at = ? WHERE operation_key = ? AND status NOT IN ('completed', 'cancelled', 'cancelled_applied')`, status, lastError, s.now().UTC().Format(time.RFC3339Nano), operationKey)
	if err != nil {
		return fmt.Errorf("agent: update REALITY inbound reset recovery: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("agent: REALITY inbound reset recovery changed unexpectedly")
	}
	return nil
}

func (s *Store) markThreeXUIResetApplied(ctx context.Context, operationKey, from string, syncUsedBytes int64) error {
	if syncUsedBytes < 0 {
		return errors.New("agent: invalid REALITY inbound reset synchronization usage")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE three_x_ui_reset_journal SET status = 'reset_applied', sync_used_bytes = ?, last_error = '', updated_at = ? WHERE operation_key = ? AND status = ?`, syncUsedBytes, s.now().UTC().Format(time.RFC3339Nano), operationKey, from)
	if err != nil {
		return fmt.Errorf("agent: record applied REALITY inbound reset: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("agent: applied REALITY inbound reset journal changed unexpectedly")
	}
	return nil
}

func (s *Store) markThreeXUIResetDisabled(ctx context.Context, operationKey, from string, syncUsedBytes int64) error {
	if syncUsedBytes < 0 {
		return errors.New("agent: invalid disabled REALITY inbound usage")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE three_x_ui_reset_journal SET status = 'disabled', sync_used_bytes = ?, last_error = '', updated_at = ? WHERE operation_key = ? AND status = ?`, syncUsedBytes, s.now().UTC().Format(time.RFC3339Nano), operationKey, from)
	if err != nil {
		return fmt.Errorf("agent: record disabled REALITY inbound usage: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("agent: disabled REALITY inbound reset journal changed unexpectedly")
	}
	return nil
}

func threeXUIResetJournalApplied(status string) bool {
	switch status {
	case "reset_applied", "reset_done", "enable_done", "retry_applied", "restore_pending_applied", "cancelled_applied":
		return true
	default:
		return false
	}
}

func (s *Store) transitionThreeXUIReset(ctx context.Context, operationKey, from, to, lastError string) error {
	lastError = strings.TrimSpace(lastError)
	if len(lastError) > 1024 {
		lastError = lastError[:1024]
	}
	result, err := s.db.ExecContext(ctx, `UPDATE three_x_ui_reset_journal SET status = ?, last_error = ?, updated_at = ? WHERE operation_key = ? AND status = ?`, to, lastError, s.now().UTC().Format(time.RFC3339Nano), operationKey, from)
	if err != nil {
		return fmt.Errorf("agent: update REALITY inbound reset journal: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("agent: REALITY inbound reset journal changed unexpectedly")
	}
	return nil
}
