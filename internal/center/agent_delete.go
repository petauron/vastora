package center

import (
	"context"
	"database/sql"
	"errors"
	"slices"
)

// DeleteAgent only forgets an unused, disabled node. It never dispatches remote
// cleanup, and foreign keys remain the final guard against deleting dependencies.
func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM agents WHERE id=?`, id).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("center: node not found")
		}
		return err
	}
	if status != "disabled" {
		return errors.New("center: disable node before deleting")
	}
	selection, err := readLandingSelection(ctx, tx)
	if err != nil {
		return err
	}
	if slices.Contains(selection.NodeIDs, id) {
		return errors.New("center: node still in use")
	}
	for _, query := range []string{
		`SELECT COUNT(*) FROM applications WHERE node_id=? AND status<>'stopped'`,
		`SELECT COUNT(*) FROM publications WHERE entry_node_id=? AND (status<>'stopped' OR cleanup_pending=1)`,
		`SELECT COUNT(*) FROM publications p JOIN services s ON s.id=p.service_id JOIN applications a ON a.id=s.application_id WHERE a.node_id=? AND (p.status<>'stopped' OR p.cleanup_pending=1)`,
		`SELECT COUNT(*) FROM site_gateways WHERE agent_id=?`,
		`SELECT COUNT(*) FROM cloudflare_tunnels WHERE agent_id=? AND status<>'stopped'`,
		`SELECT COUNT(*) FROM deployments WHERE agent_id=? AND (state IN ('pending','running') OR reconciliation_required=1)`,
		`SELECT COUNT(*) FROM landing_server_states WHERE node_id=? AND status<>'stopped'`,
		`SELECT COUNT(*) FROM agent_updates WHERE agent_id=? AND state IN ('pending','running','installing')`,
		`SELECT COUNT(*) FROM agent_decommissions WHERE agent_id=? AND state IN ('pending','running','cleaning')`,
	} {
		var count int
		if err := tx.QueryRowContext(ctx, query, id).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("center: node still in use")
		}
	}
	// Uninstalled applications and fully stopped entries are only historical
	// Center records. Keep active dependencies protected by the transaction/FKs.
	if _, err := tx.ExecContext(ctx, `DELETE FROM landing_proxy_states WHERE (node_id=? OR landing_node_id=?) AND status='stopped' AND desired_revision=applied_revision AND json_extract(desired_json,'$.proxy') IS NULL AND json_extract(desired_json,'$.clients') IS NULL`, id, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM three_x_ui_client_accounts WHERE controller_id IN (SELECT id FROM applications WHERE node_id=? AND status='stopped') AND pending_command_id='' AND NOT EXISTS(SELECT 1 FROM landing_client_grants WHERE parent_id=three_x_ui_client_accounts.id) AND NOT EXISTS(SELECT 1 FROM landing_client_blocks WHERE parent_id=three_x_ui_client_accounts.id)`, id); err != nil {
		return err
	}
	for _, query := range []string{
		`DELETE FROM publications WHERE entry_node_id=? AND status='stopped' AND cleanup_pending=0`,
		`DELETE FROM applications WHERE node_id=? AND status='stopped'`,
	} {
		if _, err := tx.ExecContext(ctx, query, id); err != nil {
			return errors.New("center: node could not be deleted; check remaining dependencies")
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agents WHERE id=? AND status='disabled'`, id); err != nil {
		// Do not expose SQLite schema or dependency details to the UI.
		return errors.New("center: node could not be deleted; check remaining dependencies")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key=?`, runtimeRecoverySettingsPrefix+id); err != nil {
		return err
	}
	return tx.Commit()
}
