package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

func resolveGlivelaConfigDir(dataDir string) (string, error) {
	legacy := filepath.Join(dataDir, "meridian")
	if _, err := os.Lstat(legacy); err == nil {
		return "", errors.New("agent: obsolete meridian configuration directory exists; upgrade through Vastora v0.1.0 and retire legacy mounts before starting")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return filepath.Join(dataDir, glivelaRuntimeDirectory), nil
}

// Schema 25 (Vastora v0.1.0) is structurally identical to baseline 26. Take a
// verified snapshot before advancing PRAGMA user_version so post-v0.1.0 Agent
// binaries never fall back to pre-v0.1.0 tables or encryption contexts.
func (s *Store) promoteBaselineSchema(ctx context.Context) error {
	if err := prepareAgentSchemaBackup(ctx, s.db, s.dataDir, s.key, "schema-25-backup-baseline", minSupportedAgentSchemaVersion); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version = 26`); err != nil {
		return err
	}
	return tx.Commit()
}
