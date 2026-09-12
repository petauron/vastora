package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const agentRemovalSchema = `CREATE TABLE agent_removals (
 agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 state TEXT NOT NULL CHECK(state IN ('pending','failed')),
 prepared INTEGER NOT NULL DEFAULT 0 CHECK(prepared IN (0,1)),
 headscale_done INTEGER NOT NULL DEFAULT 0 CHECK(headscale_done IN (0,1)),
 headscale_identity_json BLOB NOT NULL DEFAULT '{}',
 tunnel_done INTEGER NOT NULL DEFAULT 0 CHECK(tunnel_done IN (0,1)),
 last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
)`

var (
	errNodeRemovalOnline            = errors.New("center: permanent removal requires an offline node")
	errNodeRemovalName              = errors.New("center: node removal confirmation does not match")
	errNodeRemovalShared            = errors.New("center: node removal would affect a shared service")
	errNodeRemovalWaiting           = errors.New("center: node removal is waiting for subscription cleanup")
	errNodeRemovalControllerOffline = errors.New("center: subscription controller must be online to remove its node entry")
)

// StartAgentRemoval records irreversible administrative intent, not a remote
// uninstall receipt. No task is sent to the expired host. The durable row keeps
// cleanup retryable across HTTP cancellation, provider failure and restart.
func (s *Store) StartAgentRemoval(ctx context.Context, id, confirmation string) error {
	s.agentRemovalMu.Lock()
	defer s.agentRemovalMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var name, seen string
	if err := tx.QueryRowContext(ctx, `SELECT name,last_seen_at FROM agents WHERE id=?`, id).Scan(&name, &seen); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: node not found")
	} else if err != nil {
		return err
	}
	if strings.TrimSpace(confirmation) != strings.TrimSpace(name) || strings.TrimSpace(name) == "" {
		return errNodeRemovalName
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_removals WHERE agent_id=?`, id).Scan(&existing); err != nil {
		return err
	}
	now := s.now().UTC()
	if existing == 0 {
		lastSeen, parseErr := time.Parse(time.RFC3339Nano, seen)
		if parseErr != nil || lastSeen.After(now.Add(-agentConnectedMaxAge)) {
			return errNodeRemovalOnline
		}
		if err := validateAgentRemovalDependencies(ctx, tx, id); err != nil {
			return err
		}
		// Retain application history until remote/controller cleanup succeeds.
		// Disabling and revoking in the same transaction fences a late reconnect.
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET status='disabled',credential_revoked_at=? WHERE id=?`, now.Format(time.RFC3339Nano), id); err != nil {
			return err
		}
		if err := revokeAgentReconnectGrants(ctx, tx, id); err != nil {
			return err
		}
		if err := cancelRemovedAgentTasks(ctx, tx, id, now); err != nil {
			return err
		}
	} else {
		// Explicit retry preserves task identity and receipt reconciliation on
		// the surviving controller; it never creates competing remove tasks.
		if err := retryRemovedAgentCommands(ctx, tx, id, now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_removals(agent_id,state,created_at,updated_at) VALUES(?,'pending',?,?)
	 ON CONFLICT(agent_id) DO UPDATE SET state='pending',last_error='',updated_at=excluded.updated_at`, id, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.taskChanges.notify("agent:" + id)
	return nil
}

func validateAgentRemovalDependencies(ctx context.Context, tx *sql.Tx, id string) error {
	// Removing one expired worker must never implicitly destroy a controller,
	// switch another node's egress to direct, or take down another app's entry.
	checks := []string{
		`SELECT COUNT(*) FROM application_commands c JOIN applications a ON a.id=c.application_id WHERE a.node_id=? AND c.agent_id<>a.node_id AND c.kind<>'3xui.node.reconcile' AND (c.state='running' OR c.reconciliation_required=1)`,
		`SELECT COUNT(*) FROM landing_client_grants g JOIN three_x_ui_client_accounts p ON p.id=g.parent_id JOIN applications a ON a.id=p.controller_id JOIN applications e ON e.id=g.application_id WHERE a.node_id=? AND e.node_id<>a.node_id`,
		`SELECT COUNT(*) FROM three_x_ui_nodes n JOIN applications a ON a.id=n.master_application_id JOIN applications w ON w.id=n.worker_application_id WHERE a.node_id=? AND w.node_id<>a.node_id AND (w.status<>'stopped' OR n.status<>'stopped')`,
		`SELECT COUNT(*) FROM publications p JOIN services s ON s.id=p.service_id JOIN applications a ON a.id=s.application_id WHERE p.entry_node_id=? AND a.node_id<>p.entry_node_id AND (p.status<>'stopped' OR p.cleanup_pending=1)`,
		`SELECT COUNT(*) FROM landing_proxy_states WHERE landing_node_id=? AND node_id<>landing_node_id AND (status<>'stopped' OR desired_revision<>applied_revision OR json_extract(desired_json,'$.proxy') IS NOT NULL OR json_extract(desired_json,'$.clients') IS NOT NULL)`,
		`SELECT COUNT(*) FROM landing_proxy_retirements WHERE landing_node_id=? AND node_id<>landing_node_id`,
		`SELECT COUNT(*) FROM landing_client_grants g JOIN applications a ON a.id=g.application_id WHERE g.landing_node_id=? AND a.node_id<>g.landing_node_id AND g.status<>'revoked'`,
		`SELECT COUNT(*) FROM three_x_ui_migrations m JOIN applications a ON a.id=m.source_application_id OR a.id=m.target_application_id WHERE a.node_id=? AND m.state IN ('backing_up','restoring','switching')`,
		`SELECT COUNT(*) FROM applications a WHERE a.node_id=? AND a.app_key='vastora-official/pulse' AND EXISTS(SELECT 1 FROM applications p WHERE p.node_id<>a.node_id AND p.app_key='vastora-official/pulse-agent' AND p.status<>'stopped')`,
		`SELECT COUNT(*) FROM agent_network_candidates c JOIN settings s ON s.key='cloudflare_setup_gateway_binding' WHERE c.agent_id=? AND c.address=json_extract(s.value,'$.bindAddress')`,
	}
	for _, query := range checks {
		var count int
		if err := tx.QueryRowContext(ctx, query, id).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return errNodeRemovalShared
		}
	}
	return nil
}

func revokeAgentReconnectGrants(ctx context.Context, tx *sql.Tx, id string) error {
	for _, query := range []string{
		`DELETE FROM secrets WHERE id IN (SELECT bootstrap_secret_id FROM agent_enrollment_tokens WHERE target_agent_id=? AND bootstrap_secret_id IS NOT NULL)`,
		`DELETE FROM agent_enrollment_tokens WHERE target_agent_id=?`,
		`DELETE FROM agent_enrollment_operations WHERE agent_id=?`,
	} {
		if _, err := tx.ExecContext(ctx, query, id); err != nil {
			return err
		}
	}
	return nil
}

func cancelRemovedAgentTasks(ctx context.Context, tx *sql.Tx, id string, now time.Time) error {
	for _, table := range []string{"deployments", "application_commands"} {
		// Only the revoked executor is abandoned. Commands running on the shared
		// subscription controller still need their normal result/reconciliation.
		query := `UPDATE ` + table + ` SET state='failed',reconciliation_required=0,reconciliation_requested=0,lease_expires_at='',error='node permanently removed; remote cleanup not performed',updated_at=? WHERE agent_id=? AND (state IN ('pending','running') OR reconciliation_required=1)`
		if _, err := tx.ExecContext(ctx, query, now.Format(time.RFC3339Nano), id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state='failed',error='target node permanently removed',updated_at=? WHERE state='pending' AND attempt=0 AND reconciliation_required=0 AND application_id IN(SELECT id FROM applications WHERE node_id=?) AND agent_id<>? AND NOT(kind='3xui.node.reconcile' AND json_extract(input_json,'$.action')='remove')`, now.Format(time.RFC3339Nano), id, id); err != nil {
		return err
	}
	// Pulse prepares a short-lived enrollment token on its shared service, but
	// gateway_node_id is the collector's actual owner. It must not enqueue work
	// or retain a live enrollment task for a permanently revoked collector.
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state='failed',reconciliation_required=0,reconciliation_requested=0,lease_expires_at='',error='target node permanently removed',updated_at=? WHERE gateway_node_id=? AND kind='pulse.enrollment.create' AND (state IN('pending','running') OR reconciliation_required=1)`, now.Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_updates SET state='failed',lease_expires_at='',last_error='node permanently removed',updated_at=? WHERE agent_id=? AND state IN ('pending','running','installing')`, now.Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE agent_decommissions SET state='abandoned',callback_token_hash=X'',lease_expires_at='',last_error='node permanently removed; remote cleanup not performed',updated_at=? WHERE agent_id=? AND state<>'succeeded'`, now.Format(time.RFC3339Nano), id)
	return err
}

func retryRemovedAgentCommands(ctx context.Context, tx *sql.Tx, id string, now time.Time) error {
	// Retry only the latest unresolved command for each retired application.
	// Old failed attempts must not supersede a later successful receipt.
	rows, err := tx.QueryContext(ctx, `SELECT c.id FROM application_commands c JOIN applications a ON a.id=c.application_id JOIN three_x_ui_nodes n ON n.worker_application_id=a.id
	 WHERE a.node_id=? AND n.status<>'stopped' AND c.kind=? AND c.state='failed'
	 AND (json_extract(c.input_json,'$.action')='remove' OR c.reconciliation_required=1)
	 AND c.rowid=(SELECT latest.rowid FROM application_commands latest WHERE latest.application_id=c.application_id AND latest.kind=c.kind ORDER BY latest.created_at DESC,latest.rowid DESC LIMIT 1)`, id, nodeCommandKind)
	if err != nil {
		return err
	}
	commands, err := scanRemovalStrings(rows)
	if err != nil {
		return err
	}
	for _, command := range commands {
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE agent_id=(SELECT agent_id FROM application_commands WHERE id=?) AND id<>? AND kind NOT IN('3xui.controller.manage','pulse.enrollment.create') AND (state IN('pending','running') OR reconciliation_required=1)`, command, command).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return errors.New("center: subscription controller has another operation in progress")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state='pending',reconciliation_requested=reconciliation_required,lease_expires_at='',error='',updated_at=? WHERE id=?`, now.Format(time.RFC3339Nano), command); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RunAgentRemovals(ctx context.Context, report func(error)) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.resumeAgentRemovals(ctx); err != nil && report != nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Store) resumeAgentRemovals(ctx context.Context) error {
	s.agentRemovalMu.Lock()
	defer s.agentRemovalMu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id FROM agent_removals WHERE state='pending' ORDER BY created_at LIMIT 20`)
	if err != nil {
		return err
	}
	ids, err := scanRemovalStrings(rows)
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		operationCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := s.resumeAgentRemoval(operationCtx, id)
		cancel()
		if err == nil || errors.Is(err, errNodeRemovalWaiting) {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Do not expose provider payloads, SQL or identifiers on the node card.
		if _, saveErr := s.db.ExecContext(ctx, `UPDATE agent_removals SET state='failed',last_error=?,updated_at=? WHERE agent_id=?`, err.Error(), s.now().UTC().Format(time.RFC3339Nano), id); saveErr != nil {
			failures = append(failures, saveErr)
		}
		failures = append(failures, fmt.Errorf("remove node %s: %w", id, err))
	}
	return errors.Join(failures...)
}

func (s *Store) resumeAgentRemoval(ctx context.Context, id string) error {
	if err := s.removeAgentPrivateIdentity(ctx, id); err != nil {
		return err
	}
	if err := s.prepareAgentRemoval(ctx, id); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.id,p.kind,p.entry_node_id,p.dns_provider,p.dns_record_id,p.desired_revision FROM publications p JOIN services s ON s.id=p.service_id JOIN applications a ON a.id=s.application_id WHERE (a.node_id=? OR p.entry_node_id=?) AND p.cleanup_pending=1`, id, id)
	if err != nil {
		return err
	}
	cleanups, err := publicationCleanups(rows)
	if err != nil {
		return err
	}
	if err = s.cleanupStoppedPublications(ctx, cleanups); err != nil {
		return err
	}
	var pending int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications p JOIN services s ON s.id=p.service_id JOIN applications a ON a.id=s.application_id WHERE (a.node_id=? OR p.entry_node_id=?) AND p.cleanup_pending=1`, id, id).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return errors.New("center: node entry cleanup did not finish")
	}
	if err = s.removeAgentTunnel(ctx, id); err != nil {
		return err
	}
	if err = s.removeAgentSubscriptionNodes(ctx, id); err != nil {
		return err
	}
	return s.finishAgentRemoval(ctx, id)
}

func scanRemovalStrings(rows *sql.Rows) ([]string, error) {
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) prepareAgentRemoval(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var prepared bool
	if err = tx.QueryRowContext(ctx, `SELECT prepared FROM agent_removals WHERE agent_id=?`, id).Scan(&prepared); err != nil {
		return err
	}
	if prepared {
		return nil
	}
	if err = validateAgentRemovalDependencies(ctx, tx, id); err != nil {
		return err
	}
	now := s.now().UTC()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM applications WHERE node_id=?`, id)
	if err != nil {
		return err
	}
	apps, err := scanRemovalStrings(rows)
	if err != nil {
		return err
	}
	for _, app := range apps {
		var cleanups []publicationCleanup
		// This is desired-state retirement, NOT a fabricated Agent completion.
		if err = s.completeApplication(ctx, tx, "", app, "uninstall", 0, ApplicationTaskResult{}, now, &cleanups); err != nil {
			return err
		}
	}
	if err = s.removeAgentLandingReferences(ctx, tx, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM site_gateways WHERE agent_id=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_removals SET prepared=1,updated_at=? WHERE agent_id=?`, now.Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) removeAgentSubscriptionNodes(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT n.worker_application_id FROM three_x_ui_nodes n JOIN applications a ON a.id=n.worker_application_id WHERE a.node_id=? AND n.status<>'stopped' ORDER BY n.worker_application_id`, id)
	if err != nil {
		return err
	}
	apps, err := scanRemovalStrings(rows)
	if err != nil {
		return err
	}
	waiting := false
	for _, app := range apps {
		var failed bool
		err = tx.QueryRowContext(ctx, `SELECT state='failed' AND (json_extract(input_json,'$.action')='remove' OR reconciliation_required=1) FROM application_commands WHERE application_id=? AND kind=? ORDER BY created_at DESC,rowid DESC LIMIT 1`, app, nodeCommandKind).Scan(&failed)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if failed {
			return errors.New("center: subscription node removal failed; retry cleanup")
		}
		var needsController, available bool
		var lastSeen string
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(n.remote_node_id,0)>0,a.status='active' AND a.credential_revoked_at='',a.last_seen_at FROM three_x_ui_nodes n JOIN applications m ON m.id=n.master_application_id JOIN agents a ON a.id=m.node_id WHERE n.worker_application_id=?`, app).Scan(&needsController, &available, &lastSeen); err != nil {
			return err
		}
		seen, parseErr := time.Parse(time.RFC3339Nano, lastSeen)
		if needsController && (!available || parseErr != nil || !seen.After(s.now().Add(-agentConnectedMaxAge))) {
			return errNodeRemovalControllerOffline
		}
		var active int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE application_id=? AND (state IN ('pending','running') OR reconciliation_required=1)`, app).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			waiting = true
			continue
		}
		if err = s.queueThreeXUINodeRemoval(ctx, tx, app, s.now().UTC()); err != nil {
			return err
		}
		var stopped bool
		if err = tx.QueryRowContext(ctx, `SELECT status='stopped' FROM three_x_ui_nodes WHERE worker_application_id=?`, app).Scan(&stopped); err != nil {
			return err
		}
		waiting = waiting || !stopped
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if waiting {
		var started string
		if err = s.db.QueryRowContext(ctx, `SELECT updated_at FROM agent_removals WHERE agent_id=?`, id).Scan(&started); err != nil {
			return err
		}
		startedAt, parseErr := time.Parse(time.RFC3339Nano, started)
		if parseErr != nil || s.now().Sub(startedAt) > 2*time.Minute {
			return errors.New("center: subscription cleanup has not confirmed completion; retry cleanup")
		}
		return errNodeRemovalWaiting
	}
	return nil
}

func (s *Store) removeAgentLandingReferences(ctx context.Context, tx *sql.Tx, id string) error {
	rows, err := tx.QueryContext(ctx, `SELECT landing_node_id FROM landing_proxy_states WHERE node_id=? UNION SELECT landing_node_id FROM landing_proxy_retirements WHERE node_id=? UNION SELECT landing_node_id FROM landing_client_grants WHERE application_id IN(SELECT id FROM applications WHERE node_id=?)`, id, id, id)
	if err != nil {
		return err
	}
	owners, err := scanRemovalStrings(rows)
	if err != nil {
		return err
	}
	secrets, err := removedAgentGrantSecrets(ctx, tx, id)
	if err != nil {
		return err
	}
	for _, query := range []string{
		`DELETE FROM landing_client_grants WHERE application_id IN(SELECT id FROM applications WHERE node_id=?)`,
		`DELETE FROM landing_client_blocks WHERE application_id IN(SELECT id FROM applications WHERE node_id=?)`,
		`DELETE FROM landing_proxy_states WHERE node_id=?`,
	} {
		if _, err = tx.ExecContext(ctx, query, id); err != nil {
			return err
		}
	}
	if err = deleteUnreferencedRemovalSecrets(ctx, tx, secrets); err != nil {
		return err
	}
	for _, owner := range owners {
		if owner != id {
			if err = s.refreshClientLandingSources(ctx, tx, owner); err != nil {
				return err
			}
		}
	}
	selection, err := readLandingSelection(ctx, tx)
	if err != nil {
		return err
	}
	if slices.Contains(selection.NodeIDs, id) {
		selection.NodeIDs = slices.DeleteFunc(selection.NodeIDs, func(value string) bool { return value == id })
		selection.Revision++
		encoded, err := json.Marshal(selection)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE settings SET value=? WHERE key=?`, string(encoded), landingSelectionKey); err != nil {
			return err
		}
	}
	return nil
}

func removedAgentGrantSecrets(ctx context.Context, tx *sql.Tx, id string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT credential_secret_id FROM landing_client_grants WHERE application_id IN(SELECT id FROM applications WHERE node_id=?) UNION SELECT material_secret_id FROM landing_client_grants WHERE material_secret_id IS NOT NULL AND application_id IN(SELECT id FROM applications WHERE node_id=?)`, id, id)
	if err != nil {
		return nil, err
	}
	return scanRemovalStrings(rows)
}
