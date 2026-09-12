package agent

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// Keep these predicates aligned with the receipt queries. Partial indexes keep
// acknowledged history out of the hot outbox/startup paths. Index only metadata,
// not encrypted completion payloads, to keep their memory and disk footprint small.
const taskReceiptIndexesSQL = `
	CREATE INDEX task_receipts_pending_completion ON task_receipts(updated_at, task_id)
		WHERE state IN ('completed', 'reconciliation_required');
	CREATE INDEX task_receipts_unresolved_application ON task_receipts(created_at, task_id)
		WHERE state IN ('processing', 'reconciliation_required', 'reconciliation_acknowledged') AND task_kind IN ('application.apply', 'legacy');
	CREATE INDEX task_receipts_expiry ON task_receipts(updated_at, task_id)
		WHERE state = 'acknowledged' OR (task_kind = 'agent.update' AND state = 'processing');
`

func migrateTaskReceiptIndexesV19(db *sql.DB, dataDir string) error {
	// Snapshot committed WAL data before changing the schema. This is a one-off
	// upgrade cost, not periodic receipt maintenance. Keep the backup on failure.
	backupDir, err := os.MkdirTemp(dataDir, "schema-18-backup-")
	if err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	if _, err := db.Exec(`VACUUM INTO ?`, filepath.Join(backupDir, "agent.db")); err != nil {
		return fmt.Errorf("back up database: %w", err)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(taskReceiptIndexesSQL + `PRAGMA user_version = 19;`); err != nil {
		return err
	}
	return tx.Commit()
}
