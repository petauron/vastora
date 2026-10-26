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

const schemaBaselineVersion int64 = 123

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Schema 122 is structurally identical to baseline 123. Only migration
// bookkeeping changes; business rows, secrets and identifiers stay in place.
func (s *Store) migrateSchema(ctx context.Context) error {
	version, err := sqliteSchemaVersion(ctx, s.db)
	if err != nil {
		return err
	}
	if version > centerSchemaVersion {
		return fmt.Errorf("center: database schema %d is newer than this release", version)
	}
	if version < 122 {
		return fmt.Errorf("center: database schema %d predates baseline 123; upgrade a backup with the last schema-122 release first", version)
	}
	if version == 122 {
		if err := s.compactMigrationHistory(ctx); err != nil {
			return err
		}
	}
	hasHistory, err := migrationHistoryExists(ctx, s.db)
	if err != nil {
		return err
	}
	if !hasHistory {
		return errors.New("center: existing database is missing migration history")
	}
	provider, err := newMigrationProvider(s.db)
	if err != nil {
		return err
	}
	current, target, err := provider.GetVersions(ctx)
	if err != nil {
		return fmt.Errorf("center: inspect database migrations: %w", err)
	}
	version, err = sqliteSchemaVersion(ctx, s.db)
	if err != nil {
		return err
	}
	if current != version || current < schemaBaselineVersion || current > target || target != centerSchemaVersion {
		return fmt.Errorf("center: inconsistent or unsupported database versions: history=%d schema=%d target=%d", current, version, target)
	}
	if current < target {
		backup, err := s.createMigrationBackup(ctx, current, target)
		if err != nil {
			return err
		}
		if _, err := provider.Up(ctx); err != nil {
			return fmt.Errorf("center: migrate database (backup: %s): %w", backup, err)
		}
	}
	return verifyMigratedSchema(ctx, s.db, target)
}

func (s *Store) compactMigrationHistory(ctx context.Context) error {
	// Refuse a partially upgraded or inconsistent database before any write.
	var current int64
	var applied bool
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(version_id),MIN(is_applied) FROM goose_db_version`).Scan(&current, &applied); err != nil {
		return fmt.Errorf("center: inspect pre-baseline history: %w", err)
	}
	if current != 122 || !applied {
		return fmt.Errorf("center: baseline requires completed schema 122, found history %d", current)
	}
	if err := verifyMigratedSchema(ctx, s.db, 122); err != nil {
		return err
	}
	backup, err := s.createMigrationBackup(ctx, 122, schemaBaselineVersion)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Do not drop or recreate application tables. This transaction changes only
	// the migration journal and the monotonic schema marker.
	if _, err := tx.ExecContext(ctx, `DELETE FROM goose_db_version;
 INSERT INTO goose_db_version(version_id,is_applied) VALUES(0,1),(123,1);
 PRAGMA user_version=123;`); err != nil {
		return fmt.Errorf("center: compact migration history (backup: %s): %w", backup, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("center: commit baseline (backup: %s): %w", backup, err)
	}
	return nil
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
	var foreignKeys int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return fmt.Errorf("center: verify foreign key enforcement: %w", err)
	}
	if foreignKeys != 1 {
		return errors.New("center: database foreign key enforcement is disabled")
	}

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
