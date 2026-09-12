package center

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Purge only after provider and subscription cleanup have succeeded. The
// foreign keys stay enabled; a new/unhandled dependency rolls this transaction
// back instead of silently orphaning shared records.
func (s *Store) finishAgentRemoval(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = validateAgentRemovalDependencies(ctx, tx, id); err != nil {
		return err
	}
	var ready bool
	if err = tx.QueryRowContext(ctx, `SELECT prepared=1 AND headscale_done=1 AND tunnel_done=1 FROM agent_removals WHERE agent_id=?`, id).Scan(&ready); err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("center: node removal cleanup is incomplete")
	}
	secretIDs, err := agentRemovalSecretIDs(ctx, tx, id)
	if err != nil {
		return err
	}
	for _, query := range []string{
		// A proposal is a historical reference, not authority to recreate a
		// retired installation. Preserve the conversation/audit, detach its task.
		`UPDATE change_proposals SET deployment_id=NULL,status=CASE WHEN status IN('pending','approved') THEN 'cancelled' ELSE status END WHERE deployment_id IN(SELECT id FROM deployments WHERE agent_id=?)`,
		`DELETE FROM task_events WHERE task_id IN(SELECT id FROM application_commands WHERE gateway_node_id=? OR application_id IN(SELECT id FROM applications WHERE node_id=?))`,
		`DELETE FROM three_x_ui_migrations WHERE source_application_id IN(SELECT id FROM applications WHERE node_id=?) OR target_application_id IN(SELECT id FROM applications WHERE node_id=?)`,
		`DELETE FROM three_x_ui_control_plane WHERE controller_application_id IN(SELECT id FROM applications WHERE node_id=?)`,
		`DELETE FROM landing_client_grants WHERE landing_node_id=? AND status='revoked'`,
		`DELETE FROM landing_proxy_retirements WHERE landing_node_id=?`,
		`DELETE FROM landing_proxy_states WHERE landing_node_id=? AND status='stopped'`,
		`DELETE FROM three_x_ui_client_accounts WHERE controller_id IN(SELECT id FROM applications WHERE node_id=?)`,
		`DELETE FROM application_commands WHERE gateway_node_id=? AND state NOT IN('pending','running') AND reconciliation_required=0`,
		`DELETE FROM node_listener_migration_cutovers WHERE legacy_gateway_id=? OR replacement_node_id=?`,
		`DELETE FROM tunnel_connector_migration_cutovers WHERE legacy_gateway_id=?`,
		`DELETE FROM publications WHERE entry_node_id=? AND status='stopped' AND cleanup_pending=0`,
		`DELETE FROM applications WHERE node_id=?`,
		`DELETE FROM cloudflare_tunnel_operations WHERE agent_id=?`,
		`DELETE FROM settings WHERE key='agent_runtime_recovery:'||?`,
	} {
		args := []any{id}
		if strings.Count(query, "?") == 2 {
			args = append(args, id)
		}
		if _, err = tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("center: remove retired node records: %w", err)
		}
	}
	removed, err := tx.ExecContext(ctx, `DELETE FROM agents WHERE id=? AND status='disabled' AND credential_revoked_at<>''`, id)
	if err != nil {
		return err
	}
	count, err := removed.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("center: retired node identity changed during cleanup")
	}
	// Never sweep the entire secret store. Only these installation-owned
	// candidates are considered, and shared references remain protected.
	if err = deleteUnreferencedRemovalSecrets(ctx, tx, secretIDs); err != nil {
		return err
	}
	return tx.Commit()
}

func agentRemovalSecretIDs(ctx context.Context, tx *sql.Tx, id string) ([]string, error) {
	queries := []string{
		`SELECT secret_id FROM application_secrets WHERE application_id IN(SELECT id FROM applications WHERE node_id=?)`,
		`SELECT secret_id FROM deployments WHERE agent_id=? AND secret_id IS NOT NULL`,
		`SELECT result_secret_id FROM application_commands WHERE result_secret_id IS NOT NULL AND (agent_id=? OR gateway_node_id=? OR application_id IN(SELECT id FROM applications WHERE node_id=?))`,
		`SELECT secret_id FROM application_credential_rotations WHERE secret_id IS NOT NULL AND application_id IN(SELECT id FROM applications WHERE node_id=?)`,
		`SELECT token_secret_id FROM cloudflare_tunnels WHERE agent_id=?`,
		`SELECT tunnel_secret_id FROM cloudflare_tunnel_operations WHERE agent_id=?`,
		`SELECT credential_secret_id FROM landing_client_grants WHERE landing_node_id=? AND status='revoked'`,
		`SELECT material_secret_id FROM landing_client_grants WHERE landing_node_id=? AND status='revoked' AND material_secret_id IS NOT NULL`,
	}
	values := []string{}
	for _, query := range queries {
		args := make([]any, strings.Count(query, "?"))
		for i := range args {
			args[i] = id
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		ids, err := scanRemovalStrings(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, ids...)
	}
	return values, nil
}

// Consult SQLite's actual FK declarations, including SET NULL references: a
// delete constraint alone would not protect a shared SET NULL secret owner.
func deleteUnreferencedRemovalSecrets(ctx context.Context, tx *sql.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.name,f."from" FROM sqlite_schema m JOIN pragma_foreign_key_list(m.name) f WHERE m.type='table' AND f."table"='secrets'`)
	if err != nil {
		return err
	}
	conditions := []string{}
	quote := func(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
	for rows.Next() {
		var table, column string
		if err = rows.Scan(&table, &column); err != nil {
			rows.Close()
			return err
		}
		conditions = append(conditions, `NOT EXISTS(SELECT 1 FROM `+quote(table)+` WHERE `+quote(column)+`=secrets.id)`)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	query := `DELETE FROM secrets WHERE id=?`
	if len(conditions) > 0 {
		query += ` AND ` + strings.Join(conditions, ` AND `)
	}
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, query, id); err != nil {
			return err
		}
	}
	return nil
}
