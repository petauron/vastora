package center

import (
	"context"
	"database/sql"
	"errors"
	"slices"
)

// Called only after all entry fences AND the Agent's exact-identity native
// deletion have been confirmed. Task events remain the durable audit. Never
// use this for an offline/unconfirmed revocation: its deny must remain.
func (s *Store) finishDeletedLandingParent(ctx context.Context, tx *sql.Tx, parentID string, apps []string) error {
	rows, err := tx.QueryContext(ctx, `SELECT application_id,landing_node_id,credential_secret_id,material_secret_id FROM landing_client_grants WHERE parent_id=?`, parentID)
	if err != nil {
		return err
	}
	owners, secrets := []string{}, []string{}
	for rows.Next() {
		var app, owner, credential string
		var material sql.NullString
		if err := rows.Scan(&app, &owner, &credential, &material); err != nil {
			rows.Close()
			return err
		}
		if !slices.Contains(apps, app) {
			rows.Close()
			return errors.New("center: deleted account has an unconfirmed entry")
		}
		if !slices.Contains(owners, owner) {
			owners = append(owners, owner)
		}
		secrets = append(secrets, credential)
		if material.Valid {
			secrets = append(secrets, material.String)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, statement := range []string{
		`DELETE FROM landing_client_grants WHERE parent_id=?`,
		`DELETE FROM landing_client_blocks WHERE parent_id=?`,
		`DELETE FROM three_x_ui_client_accounts WHERE id=?`,
	} {
		if _, err := tx.ExecContext(ctx, statement, parentID); err != nil {
			return err
		}
	}
	for _, id := range secrets {
		if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id=?`, id); err != nil {
			return err
		}
	}
	// Recompose from the original checkpoint and all OTHER users/forced
	// landing. Empty composition restores the original routes on the Agent.
	for _, app := range apps {
		if err := s.queueClientLandingRoutes(ctx, tx, app); err != nil {
			return err
		}
	}
	for _, owner := range owners {
		if err := s.refreshClientLandingSources(ctx, tx, owner); err != nil {
			return err
		}
	}
	return nil
}
