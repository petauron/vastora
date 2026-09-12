package center

import (
	"context"
	"database/sql"
)

const landingRetirementSchema = `CREATE TABLE landing_proxy_retirements (
 node_id TEXT NOT NULL REFERENCES landing_proxy_states(node_id) ON DELETE CASCADE,
 landing_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
 source_address TEXT NOT NULL,
 PRIMARY KEY(node_id,landing_node_id,source_address)
)`

// Only run after the current proxy revision succeeds. Retain grants across
// failure, restart and repeated switches; never infer success from a heartbeat.
func (s *Store) retireLandingSources(ctx context.Context, tx *sql.Tx, nodeID string, enabled bool) error {
	var owner, source string
	if err := tx.QueryRowContext(ctx, `SELECT landing_node_id,source_address FROM landing_proxy_states WHERE node_id=?`, nodeID).Scan(&owner, &source); err != nil {
		return err
	}
	type grant struct{ owner, source string }
	grants := []grant{}
	rows, err := tx.QueryContext(ctx, `SELECT landing_node_id,source_address FROM landing_proxy_retirements WHERE node_id=? ORDER BY landing_node_id,source_address`, nodeID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var g grant
		if err := rows.Scan(&g.owner, &g.source); err != nil {
			rows.Close()
			return err
		}
		if !enabled || g.owner != owner || g.source != source {
			grants = append(grants, g)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !enabled {
		grants = append(grants, grant{owner, source})
	}
	// Remove the retired purpose before recomputing a shared source union.
	// This transaction rolls back both changes if reconfiguration fails.
	if _, err := tx.ExecContext(ctx, `DELETE FROM landing_proxy_retirements WHERE node_id=?`, nodeID); err != nil {
		return err
	}
	for _, g := range grants {
		if err := s.removeLandingSource(ctx, tx, g.owner, g.source); err != nil {
			return err
		}
	}
	return nil
}
