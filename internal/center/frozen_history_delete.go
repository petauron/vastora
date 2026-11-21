package center

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Only exact prevalidated rows may be deleted inside the owning transaction.
// Inserts/updates remain frozen; restore the original guard before committing.
func allowFrozenHistoryDelete(ctx context.Context, tx *sql.Tx, table, column string, ids []string) (string, error) {
	if len(ids) == 0 {
		return "", nil
	}
	trigger := "retired_proxy_" + table + "_delete"
	var original string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=?`, trigger).Scan(&original); err != nil {
		return "", err
	}
	prefix := "CREATE TRIGGER " + trigger + " BEFORE DELETE ON " + table + " WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1)"
	suffix := " BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END"
	if strings.TrimSuffix(original, ";") != prefix+suffix {
		return "", errors.New("center: frozen history guard differs")
	}
	values := make([]string, len(ids))
	for i, id := range ids {
		values[i] = "'" + strings.ReplaceAll(id, "'", "''") + "'"
	}
	if _, err := tx.ExecContext(ctx, "DROP TRIGGER "+trigger); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, prefix+" AND OLD."+column+" NOT IN ("+strings.Join(values, ",")+")"+suffix); err != nil {
		return "", err
	}
	return original, nil
}

func restoreFrozenHistoryDelete(ctx context.Context, tx *sql.Tx, table, original string) error {
	if original == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "DROP TRIGGER retired_proxy_"+table+"_delete"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, original)
	return err
}
