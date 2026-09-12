package center

import (
	"context"
	"database/sql"
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
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT g.landing_node_id FROM landing_client_grants g JOIN applications a ON a.id=g.application_id WHERE (a.node_id=? OR g.landing_node_id=?) AND g.status<>'revoked'`, nodeID, nodeID)
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
		if err := s.refreshClientLandingSources(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}
