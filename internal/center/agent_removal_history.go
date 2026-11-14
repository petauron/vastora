package center

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// PurgeRemovedLandingHistory is a separate, explicit local administrator action.
// Ordinary removal continues preserving the frozen handover. This action only
// forgets an already removed landing after its independent Glivela retirement;
// it never dispatches work or treats historical enabled flags as live authority.
func (s *Store) PurgeRemovedLandingHistory(ctx context.Context, id, confirmation string, glivelaRetired bool) error {
	return s.purgeRemovedProxyHistory(ctx, id, confirmation, glivelaRetired, false)
}

// PurgeRemovedEntryHistory requires prior independent revocation of the returned entry.
func (s *Store) PurgeRemovedEntryHistory(ctx context.Context, id, confirmation string, glivelaRetired bool) error {
	return s.purgeRemovedProxyHistory(ctx, id, confirmation, glivelaRetired, true)
}

func (s *Store) purgeRemovedProxyHistory(ctx context.Context, id, confirmation string, glivelaRetired, entry bool) error {
	s.agentRemovalMu.Lock()
	defer s.agentRemovalMu.Unlock()
	refused := errors.New("center: removed landing history purge refused")
	if !s.businessRetired || !glivelaRetired || id == "" || strings.TrimSpace(confirmation) == "" {
		return refused
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var name, status, revoked, state string
	var ready bool
	if err := tx.QueryRowContext(ctx, `SELECT a.name,a.status,a.credential_revoked_at,r.state,r.prepared=1 AND r.headscale_done=1 AND r.tunnel_done=1 FROM agents a JOIN agent_removals r ON r.agent_id=a.id WHERE a.id=?`, id).Scan(&name, &status, &revoked, &state, &ready); err != nil {
		return refused
	}
	if name != confirmation || status != "disabled" || revoked == "" || state != "complete" || !ready {
		return refused
	}
	if err := validateAgentRemovalDependencies(ctx, tx, id, true); err != nil {
		return err
	}
	// The explicit entry operation must match an actual retained endpoint. Controllers are not inferred.
	var entries, active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_endpoints e JOIN applications a ON a.id=e.application_id WHERE a.node_id=?`, id).Scan(&entries); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM applications WHERE node_id=? AND status<>'stopped'`, id).Scan(&active); err != nil {
		return err
	}
	var entryApps, controllers int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(app_key='vastora-official/glivela-entry'),0),COALESCE(SUM(app_key IN('vastora-official/glivela','vastora-official/glivela-controller','vastora-official/meridian')),0) FROM applications WHERE node_id=?`, id).Scan(&entryApps, &controllers); err != nil {
		return err
	}
	if (!entry && entries != 0) || (entry && entries == 0 && entryApps == 0) || controllers != 0 || active != 0 {
		return refused
	}
	secretIDs, err := agentRemovalSecretIDs(ctx, tx, id)
	if err != nil {
		return err
	}
	if entry {
		for _, column := range []string{"protocol_secret_id", "hy2_auth_secret_id"} {
			rows, err := tx.QueryContext(ctx, "SELECT c."+column+" FROM meridian_credentials c JOIN meridian_endpoints e ON e.id=c.endpoint_id JOIN applications a ON a.id=e.application_id WHERE a.node_id=? AND c."+column+" IS NOT NULL", id)
			if err != nil {
				return err
			}
			values, err := scanRemovalStrings(rows)
			if err != nil {
				return err
			}
			secretIDs = append(secretIDs, values...)
		}
	}
	// Frozen grants may still say enabled/ready. Their old secrets are cleanup
	// candidates after independent retirement, never live authorization.
	for _, query := range []string{
		`SELECT protocol_secret_id FROM meridian_credentials WHERE egress_node_id=?`,
		`SELECT credential_secret_id FROM landing_client_grants WHERE landing_node_id=?`,
		`SELECT material_secret_id FROM landing_client_grants WHERE landing_node_id=? AND material_secret_id IS NOT NULL`,
	} {
		rows, err := tx.QueryContext(ctx, query, id)
		if err != nil {
			return err
		}
		values, err := scanRemovalStrings(rows)
		if err != nil {
			return err
		}
		secretIDs = append(secretIDs, values...)
	}
	// Snapshot exact primary keys before deleting parents. Exceptions never allow
	// updates/inserts or deletion of another row, and cannot escape this SQLite
	// transaction. All original guards are restored before it becomes visible.
	type scope struct {
		table, column, query string
		ids                  []string
		original             string
	}
	scopes := []scope{
		{table: "meridian_route_health_events", column: "id", query: `SELECT h.id FROM meridian_route_health_events h JOIN meridian_route_grants g ON g.id=h.route_id WHERE ? IN(g.egress_node_id,g.ipv4_egress_node_id)`},
		{table: "meridian_line_usage", column: "credential_id", query: `SELECT u.credential_id FROM meridian_line_usage u JOIN meridian_credentials c ON c.id=u.credential_id WHERE c.egress_node_id=?`},
		{table: "meridian_usage_watermarks", column: "credential_id", query: `SELECT u.credential_id FROM meridian_usage_watermarks u JOIN meridian_credentials c ON c.id=u.credential_id WHERE c.egress_node_id=?`},
		{table: "meridian_route_grants", column: "id", query: `SELECT id FROM meridian_route_grants WHERE ? IN(egress_node_id,ipv4_egress_node_id)`},
		{table: "meridian_credentials", column: "id", query: `SELECT id FROM meridian_credentials WHERE egress_node_id=?`},
		{table: "landing_client_grants", column: "id", query: `SELECT id FROM landing_client_grants WHERE landing_node_id=?`},
		{table: "landing_proxy_retirements", column: "rowid", query: `SELECT rowid FROM landing_proxy_retirements WHERE landing_node_id=?`},
		{table: "landing_proxy_states", column: "node_id", query: `SELECT node_id FROM landing_proxy_states WHERE landing_node_id=? OR applied_landing_node_id=? OR node_id=?`},
		{table: "landing_server_states", column: "node_id", query: `SELECT node_id FROM landing_server_states WHERE node_id=?`},
		{table: "node_egress_policies", column: "node_id", query: `SELECT node_id FROM node_egress_policies WHERE node_id=?`},
	}
	if entry {
		endpoint := "SELECT e.id FROM meridian_endpoints e JOIN applications a ON a.id=e.application_id WHERE a.node_id=?"
		for i := range scopes {
			switch scopes[i].table {
			case "meridian_route_health_events":
				scopes[i].query += " OR g.endpoint_id IN(" + endpoint + ")"
			case "meridian_line_usage", "meridian_usage_watermarks":
				scopes[i].query += " OR c.endpoint_id IN(" + endpoint + ")"
			case "meridian_route_grants", "meridian_credentials":
				scopes[i].query += " OR endpoint_id IN(" + endpoint + ")"
			}
		}
		scopes = append(scopes, scope{table: "meridian_endpoints", column: "id", query: "SELECT id FROM meridian_endpoints WHERE id IN(" + endpoint + ")"})
	}
	for i := range scopes {
		item := &scopes[i]
		args := make([]any, strings.Count(item.query, "?"))
		for j := range args {
			args[j] = id
		}
		rows, err := tx.QueryContext(ctx, item.query, args...)
		if err != nil {
			return err
		}
		item.ids, err = scanRemovalStrings(rows)
		if err != nil {
			return err
		}
		if len(item.ids) == 0 {
			continue
		}
		trigger := "retired_proxy_" + item.table + "_delete"
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='trigger' AND name=?`, trigger).Scan(&item.original); err != nil {
			return err
		}
		prefix := "CREATE TRIGGER " + trigger + " BEFORE DELETE ON " + item.table + " WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1)"
		suffix := " BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END"
		if strings.TrimSuffix(item.original, ";") != prefix+suffix {
			return errors.New("center: frozen history guard differs")
		}
		values := make([]string, len(item.ids))
		for j, key := range item.ids {
			values[j] = "'" + strings.ReplaceAll(key, "'", "''") + "'"
		}
		if _, err := tx.ExecContext(ctx, "DROP TRIGGER "+trigger); err != nil {
			return err
		}
		guard := prefix + " AND OLD." + item.column + " NOT IN (" + strings.Join(values, ",") + ")" + suffix
		if _, err := tx.ExecContext(ctx, guard); err != nil {
			return err
		}
	}
	// Removing a credential used by a surviving composite route would affect an
	// unrelated history owner. Foreign keys also remain enabled throughout.
	var shared int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_route_grants g JOIN meridian_credentials c ON c.id IN(g.base_credential_id,g.route_credential_id) WHERE c.egress_node_id=? AND g.egress_node_id<>? AND COALESCE(g.ipv4_egress_node_id,'')<>?`, id, id, id).Scan(&shared); err != nil {
		return err
	}
	if shared != 0 {
		return refused
	}
	if entry {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_route_grants g JOIN meridian_credentials c ON c.id IN(g.base_credential_id,g.route_credential_id) WHERE c.endpoint_id IN(SELECT e.id FROM meridian_endpoints e JOIN applications a ON a.id=e.application_id WHERE a.node_id=?) AND g.endpoint_id<>c.endpoint_id`, id).Scan(&shared); err != nil {
			return err
		}
	}
	if shared != 0 {
		return refused
	}
	for _, item := range scopes {
		for _, key := range item.ids {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+item.table+" WHERE "+item.column+"=?", key); err != nil {
				return fmt.Errorf("center: purge removed landing %s: %w", item.table, err)
			}
		}
	}
	if err := deleteAgentRemovalRecords(ctx, tx, id); err != nil {
		return err
	}
	if err := deleteUnreferencedRemovalSecrets(ctx, tx, secretIDs); err != nil {
		return err
	}
	for _, item := range scopes {
		if item.original == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, "DROP TRIGGER retired_proxy_"+item.table+"_delete"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, item.original); err != nil {
			return err
		}
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	broken := rows.Next()
	readErr := rows.Err()
	rows.Close()
	if broken || readErr != nil {
		return errors.New("center: removed landing history has unresolved references")
	}
	return tx.Commit()
}
