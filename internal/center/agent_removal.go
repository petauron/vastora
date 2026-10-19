package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	errNodeRemovalOnline       = errors.New("center: permanent removal requires an offline node")
	errNodeRemovalName         = errors.New("center: node removal confirmation does not match")
	errNodeRemovalShared       = errors.New("center: node removal would affect a shared service")
	errNodeRemovalRetainedData = errors.New("center: node retains application data; retire or migrate that data before permanent removal")
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
		if err := validateAgentRemovalDependencies(ctx, tx, id); err != nil {
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
	if err := validateRetainedRemovalData(ctx, tx, id); err != nil {
		return err
	}
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
		`SELECT COUNT(*) FROM meridian_route_grants g JOIN meridian_endpoints e ON e.id=g.endpoint_id JOIN applications a ON a.id=e.application_id WHERE ? IN (g.egress_node_id,g.ipv4_egress_node_id) AND a.node_id<>g.egress_node_id AND g.status<>'revoked'`,
		`SELECT COUNT(*) FROM meridian_credentials c JOIN meridian_endpoints e ON e.id=c.endpoint_id JOIN applications a ON a.id=e.application_id WHERE c.egress_node_id=? AND a.node_id<>c.egress_node_id AND c.enabled=1`,
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
		`UPDATE agent_reinstall_operations SET state='superseded',sealed_enrollment=NULL,last_error='Recovery command revoked by administrator' WHERE agent_id=? AND state IN ('preparing','awaiting_enrollment')`,
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
		if err == nil {
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
	// Recheck jobs created before this upgrade before touching providers.
	if err := validateRetainedRemovalData(ctx, s.db, id); err != nil {
		return err
	}
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
		if err = s.retireApplicationManagement(ctx, tx, app, now, &cleanups); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM site_gateways WHERE agent_id=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_removals SET prepared=1,updated_at=? WHERE agent_id=?`, now.Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	return tx.Commit()
}

// Legacy storage references are not permission to run proxy cleanup. Keep the
// records intact until their application-owned inventory has been retired.
// This guard never modifies component health, routes, credentials or history.
func validateRetainedRemovalData(ctx context.Context, q networkQueryer, id string) error {
	var retained bool
	err := q.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM three_x_ui_nodes n JOIN applications a ON a.id=n.worker_application_id WHERE a.node_id=? AND n.status<>'stopped')
 OR EXISTS(SELECT 1 FROM meridian_endpoints e JOIN applications a ON a.id=e.application_id WHERE a.node_id=? AND e.status<>'retired')
 OR EXISTS(SELECT 1 FROM meridian_credentials c JOIN meridian_endpoints e ON e.id=c.endpoint_id JOIN applications a ON a.id=e.application_id WHERE a.node_id=? AND c.enabled=1)
 OR EXISTS(SELECT 1 FROM meridian_route_grants g JOIN meridian_endpoints e ON e.id=g.endpoint_id JOIN applications a ON a.id=e.application_id WHERE a.node_id=? AND (g.enabled=1 OR g.status<>'revoked'))
 OR EXISTS(SELECT 1 FROM landing_server_states WHERE node_id=? AND status<>'stopped')
 OR EXISTS(SELECT 1 FROM landing_proxy_states WHERE node_id=? AND status<>'stopped')
 OR EXISTS(SELECT 1 FROM landing_client_grants g JOIN applications a ON a.id=g.application_id WHERE a.node_id=? AND g.status<>'revoked')`, id, id, id, id, id, id, id).Scan(&retained)
	if err != nil {
		return err
	}
	if retained {
		return errNodeRemovalRetainedData
	}
	return nil
}
