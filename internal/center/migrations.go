package center

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

const schemaBaselineVersion int64 = 3

//go:embed migrations/*.sql
var migrationFiles embed.FS

func (s *Store) migrateSchema(ctx context.Context) error {
	provider, err := newMigrationProvider(s.db)
	if err != nil {
		return err
	}
	hasHistory, err := migrationHistoryExists(ctx, s.db)
	if err != nil {
		return err
	}
	if !hasHistory {
		legacyVersion, err := sqliteSchemaVersion(ctx, s.db)
		if err != nil {
			return err
		}
		if legacyVersion < schemaBaselineVersion || legacyVersion > centerSchemaVersion {
			return fmt.Errorf("center: database schema version %d cannot be upgraded by this release", legacyVersion)
		}
		if err := s.initializeMigrationHistory(ctx, legacyVersion); err != nil {
			return err
		}
	}
	current, target, err := provider.GetVersions(ctx)
	if err != nil {
		return fmt.Errorf("center: inspect database migrations: %w", err)
	}
	if current > target || target != centerSchemaVersion {
		return fmt.Errorf("center: database migration range %d to %d is not supported by this release", current, target)
	}
	if current < target {
		backup, err := s.createMigrationBackup(ctx, current, target)
		if err != nil {
			return err
		}
		if _, err := provider.Up(ctx); err != nil {
			return fmt.Errorf("center: migrate database from %d to %d (backup: %s): %w", current, target, backup, err)
		}
	}
	return verifyMigratedSchema(ctx, s.db, target)
}

func newMigrationProvider(db *sql.DB) (*goose.Provider, error) {
	migrations, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("center: load embedded database migrations: %w", err)
	}
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		db,
		migrations,
		goose.WithDisableGlobalRegistry(true),
		goose.WithGoMigrations(goose.NewGoMigration(schemaBaselineVersion, nil, nil)),
		goose.WithLogger(goose.NopLogger()),
	)
	if err != nil {
		return nil, fmt.Errorf("center: configure database migrations: %w", err)
	}
	return provider, nil
}

func (s *Store) initializeMigrationHistory(ctx context.Context, version int64) error {
	store, err := database.NewStore(database.DialectSQLite3, goose.DefaultTablename)
	if err != nil {
		return fmt.Errorf("center: configure migration history: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("center: begin migration history: %w", err)
	}
	defer tx.Rollback()
	if err := store.CreateVersionTable(ctx, tx); err != nil {
		return fmt.Errorf("center: create migration history: %w", err)
	}
	if err := store.Insert(ctx, tx, database.InsertRequest{Version: 0}); err != nil {
		return fmt.Errorf("center: initialize migration history: %w", err)
	}
	for applied := schemaBaselineVersion; applied <= version; applied++ {
		if err := store.Insert(ctx, tx, database.InsertRequest{Version: applied}); err != nil {
			return fmt.Errorf("center: record database version %d: %w", applied, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("center: commit migration history: %w", err)
	}
	return nil
}

func migrationHistoryExists(ctx context.Context, db *sql.DB) (bool, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)`, goose.DefaultTablename).Scan(&exists); err != nil {
		return false, fmt.Errorf("center: inspect migration history: %w", err)
	}
	return exists, nil
}

func sqliteSchemaVersion(ctx context.Context, db *sql.DB) (int64, error) {
	var version int64
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("center: inspect SQLite schema version: %w", err)
	}
	return version, nil
}

func (s *Store) createMigrationBackup(ctx context.Context, from, to int64) (string, error) {
	directory := filepath.Join(s.dataDir, "migration-backups")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("center: create migration backup directory: %w", err)
	}
	backup := filepath.Join(directory, fmt.Sprintf("center-v%d-before-v%d-%s.db", from, to, time.Now().UTC().Format("20060102T150405.000000000Z")))
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, backup); err != nil {
		return "", fmt.Errorf("center: create pre-migration database backup: %w", err)
	}
	if err := os.Chmod(backup, 0o600); err != nil {
		return "", fmt.Errorf("center: protect pre-migration database backup: %w", err)
	}
	return backup, nil
}

func verifyMigratedSchema(ctx context.Context, db *sql.DB, expected int64) error {
	version, err := sqliteSchemaVersion(ctx, db)
	if err != nil {
		return err
	}
	if version != expected {
		return fmt.Errorf("center: migrated SQLite schema is version %d, expected %d", version, expected)
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fmt.Errorf("center: verify migrated foreign keys: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("center: migrated database contains a foreign key violation")
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("center: verify migrated foreign keys: %w", err)
	}
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return fmt.Errorf("center: verify migrated database integrity: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("center: migrated database failed integrity check: %s", integrity)
	}
	return nil
}
