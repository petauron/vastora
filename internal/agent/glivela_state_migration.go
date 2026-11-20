package agent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/petauron/vastora/internal/secret"
)

const (
	glivelaRuntimeSchemaVersion = 25
	legacyRuntimeStateAAD       = "agent-meridian-runtime"
)

// Older Docker containers bind this directory directly. Keep using the
// original path while those resources exist, including after a host restart.
func resolveGlivelaConfigDir(dataDir string) (string, error) {
	current := filepath.Join(dataDir, glivelaRuntimeDirectory)
	legacy := filepath.Join(dataDir, "meridian")
	legacyInfo, err := os.Lstat(legacy)
	if errors.Is(err, os.ErrNotExist) {
		return current, nil
	}
	if err != nil {
		return "", err
	}
	if !legacyInfo.IsDir() {
		return "", errors.New("agent: legacy Glivela configuration path is not a directory")
	}
	if _, err := os.Lstat(current); err == nil {
		return "", errors.New("agent: both legacy and current Glivela configuration directories exist")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return legacy, nil
}

// The schema version selects exactly one encryption context. Version 25
// never tries the old context if the current ciphertext fails authentication.
func (s *Store) migrateGlivelaRuntimeState(ctx context.Context) error {
	if err := prepareAgentSchemaBackup(ctx, s.db, s.dataDir, s.key, "schema-24-backup-glivela", 24); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sealed []byte
	err = tx.QueryRowContext(ctx, `SELECT sealed_state FROM meridian_runtime_state WHERE id=1`).Scan(&sealed)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		raw, err := secret.Open(s.key, sealed, []byte(legacyRuntimeStateAAD))
		if err != nil {
			return err
		}
		var state glivelaRuntimeState
		if json.Unmarshal(raw, &state) != nil || state.validate() != nil {
			return errors.New("agent: invalid legacy Glivela runtime state")
		}
		migrated, err := secret.Seal(s.key, raw, []byte(glivelaRuntimeStateAAD))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_runtime_state SET sealed_state=? WHERE id=1`, migrated); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT sealed_state FROM meridian_runtime_state WHERE id=1`).Scan(&sealed); err != nil {
			return err
		}
		reopened, err := secret.Open(s.key, sealed, []byte(glivelaRuntimeStateAAD))
		if err != nil || !bytes.Equal(reopened, raw) {
			return errors.New("agent: failed to verify migrated Glivela runtime state")
		}
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE meridian_runtime_state RENAME TO glivela_runtime_state; PRAGMA user_version=25`); err != nil {
		return err
	}
	return tx.Commit()
}
