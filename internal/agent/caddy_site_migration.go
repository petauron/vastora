package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// A retry reuses the one consistent pre-migration recovery point only while its
// logical state still matches. A changed source requires explicit review, not
// another unbounded series of snapshots or overwriting the previous snapshot.
func prepareSharedCaddyMigrationBackup(ctx context.Context, db *sql.DB, directory string, key []byte) error {
	// Resolve only the caller-selected root, as receiptPath does for macOS
	// /var. Descendants remain subject to the normal no-symlink ownership check.
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return err
	}
	directory = resolved
	path := filepath.Join(directory, "schema-23-backup-shared-caddy", "agent.db")
	if err := checkPackageParents(path); err != nil {
		return err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		var pages, pageSize uint64
		if err := db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
			return err
		}
		if err := db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
			return err
		}
		if err := requireWebsiteSpace(directory, pages*pageSize+(16<<20)); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
			return err
		}
		if err := os.Chmod(path, 0600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("agent: invalid schema recovery point")
	}
	recovery, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer recovery.Close()
	if err := VerifyRecoveryDatabaseKey(ctx, recovery, key); err != nil {
		return err
	}
	var version int
	if err := recovery.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil || version != 23 {
		return errors.New("agent: recovery schema does not match the migration")
	}
	before, err := agentLogicalDatabaseHash(ctx, recovery)
	if err != nil {
		return err
	}
	current, err := agentLogicalDatabaseHash(ctx, db)
	if err != nil {
		return err
	}
	if before != current {
		return errors.New("agent: source changed since its shared Caddy recovery point; review before retrying")
	}
	return nil
}

func agentLogicalDatabaseHash(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, `SELECT name,sql FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return "", err
	}
	type table struct{ name, statement string }
	var tables []table
	for rows.Next() {
		var t table
		if err := rows.Scan(&t.name, &t.statement); err != nil {
			rows.Close()
			return "", err
		}
		tables = append(tables, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	for _, table := range tables {
		encoded, _ := json.Marshal(table.statement)
		hash.Write(encoded)
		quoted := `"` + strings.ReplaceAll(table.name, `"`, `""`) + `"`
		rows, err := db.QueryContext(ctx, `SELECT * FROM `+quoted+` ORDER BY rowid`)
		if err != nil {
			return "", err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return "", err
		}
		for rows.Next() {
			values, destinations := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				rows.Close()
				return "", err
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				rows.Close()
				return "", err
			}
			hash.Write(encoded)
			io.WriteString(hash, "\n")
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	return string(hash.Sum(nil)), nil
}
