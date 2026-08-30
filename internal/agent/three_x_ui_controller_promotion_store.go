package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/secret"
)

type threeXUIControllerPromotionRecovery struct {
	OriginalDatabase []byte          `json:"originalDatabase"`
	TransformedDB    []byte          `json:"transformedDatabase"`
	OriginalSecrets  json.RawMessage `json:"originalSecrets"`
	OldToken         string          `json:"oldToken"`
	NewToken         string          `json:"newToken"`
}

type threeXUIControllerPromotion struct {
	MigrationID   string
	TaskID        string
	ApplicationID string
	Phase         string
	CommandHash   []byte
	Recovery      threeXUIControllerPromotionRecovery
}

func threeXUIControllerCommandHash(command ThreeXUIControllerCommandTask) ([]byte, error) {
	encoded, err := json.Marshal(command)
	if err != nil {
		return nil, fmt.Errorf("agent: hash 3x-ui controller promotion: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return digest[:], nil
}

func (s *Store) BeginThreeXUIControllerPromotion(ctx context.Context, taskID string, command ThreeXUIControllerCommandTask, recovery threeXUIControllerPromotionRecovery) (threeXUIControllerPromotion, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || command.MigrationID == "" || command.ApplicationID == "" || len(recovery.OriginalDatabase) < 16 || len(recovery.TransformedDB) < 16 || recovery.OldToken == "" || recovery.NewToken == "" {
		return threeXUIControllerPromotion{}, errors.New("agent: incomplete 3x-ui controller promotion recovery state")
	}
	hash, err := threeXUIControllerCommandHash(command)
	if err != nil {
		return threeXUIControllerPromotion{}, err
	}
	if existing, found, err := s.ThreeXUIControllerPromotion(ctx); err != nil {
		return threeXUIControllerPromotion{}, err
	} else if found {
		if existing.MigrationID != command.MigrationID || existing.TaskID != taskID || existing.ApplicationID != command.ApplicationID || !bytes.Equal(existing.CommandHash, hash) {
			return threeXUIControllerPromotion{}, errors.New("agent: another 3x-ui controller promotion is pending recovery")
		}
		return existing, nil
	}
	encoded, err := json.Marshal(recovery)
	if err != nil {
		return threeXUIControllerPromotion{}, err
	}
	sealed, err := secret.Seal(s.key, encoded, threeXUIControllerPromotionAAD(command.MigrationID))
	if err != nil {
		return threeXUIControllerPromotion{}, fmt.Errorf("agent: encrypt 3x-ui controller promotion recovery state: %w", err)
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO three_x_ui_controller_promotions(
		id, migration_id, task_id, application_id, command_hash, phase, sealed_state, created_at, updated_at
	) VALUES(1, ?, ?, ?, ?, 'prepared', ?, ?, ?)`, command.MigrationID, taskID, command.ApplicationID, hash, sealed, now, now); err != nil {
		return threeXUIControllerPromotion{}, fmt.Errorf("agent: persist 3x-ui controller promotion before import: %w", err)
	}
	return threeXUIControllerPromotion{MigrationID: command.MigrationID, TaskID: taskID, ApplicationID: command.ApplicationID, Phase: "prepared", CommandHash: hash, Recovery: recovery}, nil
}

func (s *Store) ThreeXUIControllerPromotion(ctx context.Context) (threeXUIControllerPromotion, bool, error) {
	var promotion threeXUIControllerPromotion
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT migration_id, task_id, application_id, command_hash, phase, sealed_state
		FROM three_x_ui_controller_promotions WHERE id = 1`).Scan(&promotion.MigrationID, &promotion.TaskID, &promotion.ApplicationID, &promotion.CommandHash, &promotion.Phase, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return threeXUIControllerPromotion{}, false, nil
	}
	if err != nil {
		return threeXUIControllerPromotion{}, false, fmt.Errorf("agent: read 3x-ui controller promotion: %w", err)
	}
	if threeXUIControllerPromotionPhaseIndex(promotion.Phase) < 0 {
		return threeXUIControllerPromotion{}, false, errors.New("agent: stored 3x-ui controller promotion phase is invalid")
	}
	encoded, err := secret.Open(s.key, sealed, threeXUIControllerPromotionAAD(promotion.MigrationID))
	if err != nil || json.Unmarshal(encoded, &promotion.Recovery) != nil || len(promotion.Recovery.OriginalDatabase) < 16 || len(promotion.Recovery.TransformedDB) < 16 || promotion.Recovery.OldToken == "" || promotion.Recovery.NewToken == "" {
		return threeXUIControllerPromotion{}, false, errors.New("agent: stored 3x-ui controller promotion recovery state is invalid")
	}
	return promotion, true, nil
}

func (s *Store) AdvanceThreeXUIControllerPromotion(ctx context.Context, expected, next string) error {
	if threeXUIControllerPromotionPhaseIndex(next) != threeXUIControllerPromotionPhaseIndex(expected)+1 {
		return errors.New("agent: invalid 3x-ui controller promotion phase transition")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE three_x_ui_controller_promotions SET phase = ?, last_error = '', updated_at = ? WHERE id = 1 AND phase = ?`, next, s.now().UTC().Format(time.RFC3339Nano), expected)
	if err != nil {
		return fmt.Errorf("agent: advance 3x-ui controller promotion: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("agent: 3x-ui controller promotion phase changed while applying")
	}
	return nil
}

func (s *Store) RecordThreeXUIControllerPromotionError(ctx context.Context, cause error) {
	message := strings.TrimSpace(cause.Error())
	if len(message) > 2048 {
		message = message[:2048]
	}
	_, _ = s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE three_x_ui_controller_promotions SET last_error = ?, updated_at = ? WHERE id = 1`, message, s.now().UTC().Format(time.RFC3339Nano))
}

func (s *Store) ClearThreeXUIControllerPromotion(ctx context.Context, migrationID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM three_x_ui_controller_promotions WHERE id = 1 AND migration_id = ?`, migrationID)
	if err != nil {
		return fmt.Errorf("agent: clear 3x-ui controller promotion: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("agent: 3x-ui controller promotion changed before cleanup")
	}
	return nil
}

func (s *Store) resumableThreeXUIControllerPromotion(ctx context.Context, task DeploymentTask) (bool, error) {
	if task.Kind != "application.command" || task.ControllerCommand == nil || task.ControllerCommand.Action != "promote" {
		return false, nil
	}
	promotion, found, err := s.ThreeXUIControllerPromotion(ctx)
	if err != nil || !found {
		return false, err
	}
	hash, err := threeXUIControllerCommandHash(*task.ControllerCommand)
	if err != nil {
		return false, err
	}
	return promotion.TaskID == task.ID && promotion.MigrationID == task.ControllerCommand.MigrationID && bytes.Equal(promotion.CommandHash, hash), nil
}

func threeXUIControllerPromotionPhaseIndex(phase string) int {
	for index, candidate := range []string{"prepared", "imported", "api_ready", "role_configured", "applied"} {
		if phase == candidate {
			return index
		}
	}
	return -1
}

func threeXUIControllerPromotionAAD(migrationID string) []byte {
	return []byte("agent-3x-ui-controller-promotion:" + migrationID)
}
