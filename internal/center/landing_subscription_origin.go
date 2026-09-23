package center

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

// Switch only an enrolled controller's subscription service, after its Agent
// reports the private in-process endpoint. Native panel/public entry ports and
// unrelated subscription controllers are not changed.
func (s *Store) reconcileLandingSubscriptionOrigin(ctx context.Context, tx *sql.Tx, nodeID string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT a.id,s.id,s.endpoint FROM applications a JOIN services s ON s.application_id=a.id
		JOIN landing_client_capabilities c ON c.node_id=a.node_id
		WHERE a.node_id=? AND s.name='subscription' AND s.status<>'stopped' AND c.generation=?
		AND EXISTS(SELECT 1 FROM three_x_ui_client_accounts p JOIN landing_client_grants g ON g.parent_id=p.id WHERE p.controller_id=a.id AND g.applied_revision>0)`, nodeID, landing.ClientRuntimeGeneration)
	if err != nil {
		return err
	}
	type service struct{ app, id, endpoint string }
	values := []service{}
	for rows.Next() {
		var value service
		if err := rows.Scan(&value.app, &value.id, &value.endpoint); err != nil {
			rows.Close()
			return err
		}
		values = append(values, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, value := range values {
		host, port, err := net.SplitHostPort(value.endpoint)
		if err != nil {
			return err
		}
		if port == strconv.Itoa(landing.SubscriptionPort) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE services SET endpoint=?,updated_at=? WHERE id=?`, net.JoinHostPort(host, strconv.Itoa(landing.SubscriptionPort)), now.UTC().Format(time.RFC3339Nano), value.id); err != nil {
			return err
		}
		if err := s.reconcileApplicationPublications(ctx, tx, value.app, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) reconcileClientLandingSourcesForNode(ctx context.Context, tx *sql.Tx, nodeID string) error {
	if frozen, err := meridianCutoverBlocksLandingChanges(ctx, tx); err != nil {
		return err
	} else if frozen {
		return nil
	}
	// Revoked grants remain as durable cleanup markers while their target has
	// an execution fence. Include them when discovering which landing source
	// sets need to converge; refreshClientLandingSources excludes them from the
	// resulting authorization plan.
	rows, err := tx.QueryContext(ctx, `SELECT g.landing_node_id FROM landing_client_grants g
		JOIN applications a ON a.id=g.application_id WHERE a.node_id=? OR g.landing_node_id=?
		UNION SELECT g.egress_node_id FROM meridian_route_grants g
		JOIN meridian_endpoints endpoint ON endpoint.id=g.endpoint_id
		JOIN applications a ON a.id=endpoint.application_id WHERE a.node_id=? OR g.egress_node_id=?`, nodeID, nodeID, nodeID, nodeID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		// Observation of one source must not rewrite failed or unresolved
		// desired state on a different node. Explicit edits use their own path.
		var blocked bool
		if err := tx.QueryRowContext(ctx, `SELECT
			EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded')
			OR EXISTS(SELECT 1 FROM landing_server_states WHERE node_id=? AND status='failed')`, id, id).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			continue
		}
		// A successful source receipt or heartbeat must not roll back because
		// another node's derived authorization needs operator repair. Keep the
		// plan and cleanup markers atomic within this target's savepoint.
		if _, err := tx.ExecContext(ctx, `SAVEPOINT landing_source_reconciliation`); err != nil {
			return err
		}
		// A revoked row cannot be deleted until its entry has a queued plan
		// without that credential. Both route and source updates are atomic
		// with removal of the durable cleanup marker.
		reconcileErr := s.queueRevokedLandingGrantRoutes(ctx, tx, id)
		if reconcileErr == nil {
			reconcileErr = s.refreshClientLandingSources(ctx, tx, id)
		}
		if reconcileErr == nil {
			reconcileErr = s.deleteRevokedLandingGrantTombstones(ctx, tx, id)
		}
		if reconcileErr != nil {
			if _, err := tx.ExecContext(ctx, `ROLLBACK TO landing_source_reconciliation`); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `RELEASE landing_source_reconciliation`); err != nil {
			return err
		}
		if reconcileErr != nil {
			if !errors.Is(reconcileErr, errLandingSourceReconciliation) && !errors.Is(reconcileErr, errExecutionBlocked) &&
				!errors.Is(reconcileErr, errLandingRouteApplying) && !errors.Is(reconcileErr, errEntryPrivateIdentityChanged) &&
				!errors.Is(reconcileErr, errLandingPrivateIdentityChanged) {
				return reconcileErr
			}
			if _, err := tx.ExecContext(ctx, `UPDATE landing_server_states SET last_error=? WHERE node_id=?`, landingSourceReconciliationMessage, id); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE landing_server_states SET last_error='' WHERE node_id=? AND last_error=?`, id, landingSourceReconciliationMessage); err != nil {
			return err
		}
	}
	return nil
}
