package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/petauron/vastora/internal/secret"
)

const agentDatabaseKeyBindingComponent = "agent"

func inspectAgentDatabaseKeyBinding(ctx context.Context, db *sql.DB, key []byte) (bool, error) {
	exists, err := agentTableHasColumns(ctx, db, "storage_key_binding", "id", "sealed")
	if err != nil || !exists {
		return false, err
	}
	var sealed []byte
	if err := db.QueryRowContext(ctx, `SELECT sealed FROM storage_key_binding WHERE id = 1`).Scan(&sealed); errors.Is(err, sql.ErrNoRows) {
		return false, errors.New("agent: database key binding is missing")
	} else if err != nil {
		return false, fmt.Errorf("agent: read database key binding: %w", err)
	}
	if err := secret.VerifyDatabaseKeyBinding(key, sealed, agentDatabaseKeyBindingComponent); err != nil {
		return false, fmt.Errorf("agent: verify database key binding: %w", err)
	}
	return true, nil
}

func (s *Store) initializeDatabaseKeyBinding(ctx context.Context) error {
	sealed, err := secret.SealDatabaseKeyBinding(s.key, agentDatabaseKeyBindingComponent)
	if err != nil {
		return fmt.Errorf("agent: create database key binding: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO storage_key_binding(id, sealed) VALUES(1, ?) ON CONFLICT(id) DO NOTHING`, sealed); err != nil {
		return fmt.Errorf("agent: save database key binding: %w", err)
	}
	bound, err := inspectAgentDatabaseKeyBinding(ctx, s.db, s.key)
	if err != nil {
		return err
	}
	if !bound {
		return errors.New("agent: database key binding was not initialized")
	}
	return nil
}

func verifyAgentEncryptedState(ctx context.Context, db *sql.DB, key []byte) error {
	if exists, err := agentTableHasColumns(ctx, db, "applied_installations", "instance_id", "sealed_state"); err != nil {
		return err
	} else if exists {
		if err := verifyAgentCiphertextRows(ctx, db, key, `SELECT instance_id, sealed_state FROM applied_installations`, func(id string) []byte { return applicationStateContext(id) }, "applied application"); err != nil {
			return err
		}
	} else if exists, err := agentTableHasColumns(ctx, db, "applied_installations", "instance_id", "sealed_secrets"); err != nil {
		return err
	} else if exists {
		if err := verifyAgentCiphertextRows(ctx, db, key, `SELECT instance_id, sealed_secrets FROM applied_installations`, func(id string) []byte { return []byte("agent-instance:" + id) }, "legacy applied application"); err != nil {
			return err
		}
	}

	if exists, err := agentTableHasColumns(ctx, db, "control_plane_connection", "agent_id", "sealed_credential"); err != nil {
		return err
	} else if exists {
		if err := verifyAgentCiphertextRows(ctx, db, key, `SELECT agent_id, sealed_credential FROM control_plane_connection`, func(id string) []byte { return []byte("agent-control-plane:" + id) }, "control-plane credential"); err != nil {
			return err
		}
	}
	if exists, err := agentTableHasColumns(ctx, db, "control_plane_connection", "agent_id", "sealed_private_key"); err != nil {
		return err
	} else if exists {
		if err := verifyAgentCiphertextRows(ctx, db, key, `SELECT agent_id, sealed_private_key FROM control_plane_connection WHERE length(sealed_private_key) > 0`, func(id string) []byte { return []byte("agent-control-plane-key:" + id) }, "control-plane private key"); err != nil {
			return err
		}
	}
	if exists, err := agentTableHasColumns(ctx, db, "agent_install_operations", "operation_id", "sealed_token", "sealed_private_key"); err != nil {
		return err
	} else if exists {
		if err := verifyAgentCiphertextRows(ctx, db, key, `SELECT operation_id, sealed_token FROM agent_install_operations`, func(id string) []byte { return []byte("agent-install-token:" + id) }, "installation token"); err != nil {
			return err
		}
		if err := verifyAgentCiphertextRows(ctx, db, key, `SELECT operation_id, sealed_private_key FROM agent_install_operations`, func(id string) []byte { return []byte("agent-install-key:" + id) }, "installation private key"); err != nil {
			return err
		}
	}
	if exists, err := agentTableHasColumns(ctx, db, "task_receipts", "task_id", "sealed_completion"); err != nil {
		return err
	} else if exists {
		if err := verifyAgentCiphertextRows(ctx, db, key, `SELECT task_id, sealed_completion FROM task_receipts WHERE sealed_completion IS NOT NULL`, taskCompletionContext, "task completion"); err != nil {
			return err
		}
	}
	if exists, err := agentTableHasColumns(ctx, db, "gateway_applied_state", "sealed_certificates"); err != nil {
		return err
	} else if exists {
		var sealed []byte
		err := db.QueryRowContext(ctx, `SELECT sealed_certificates FROM gateway_applied_state WHERE id = 1`).Scan(&sealed)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("agent: inspect gateway certificates: %w", err)
		}
		if err == nil {
			if _, err := secret.Open(key, sealed, []byte("agent-gateway-certificates")); err != nil {
				return fmt.Errorf("agent: gateway certificates do not match the local key: %w", err)
			}
		}
	}
	return nil
}

func verifyAgentCiphertextRows(ctx context.Context, db *sql.DB, key []byte, query string, additionalData func(string) []byte, description string) error {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("agent: inspect %s: %w", description, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var sealed []byte
		if err := rows.Scan(&id, &sealed); err != nil {
			return fmt.Errorf("agent: inspect %s: %w", description, err)
		}
		if _, err := secret.Open(key, sealed, additionalData(id)); err != nil {
			return fmt.Errorf("agent: %s %s does not match the local key: %w", description, id, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("agent: inspect %s: %w", description, err)
	}
	return nil
}

func agentTableHasColumns(ctx context.Context, db *sql.DB, table string, required ...string) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info("`+strings.ReplaceAll(table, `"`, `""`)+`")`)
	if err != nil {
		return false, fmt.Errorf("agent: inspect table %s: %w", table, err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var sequence, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&sequence, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, fmt.Errorf("agent: inspect table %s: %w", table, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("agent: inspect table %s: %w", table, err)
	}
	if len(columns) == 0 {
		return false, nil
	}
	for _, column := range required {
		if !columns[column] {
			return false, nil
		}
	}
	return true, nil
}
