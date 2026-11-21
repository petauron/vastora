package center

import (
	"context"
	"database/sql"
)

type supersededHistoryScope struct {
	table, column string
	ids           []string
	frozen        bool
	original      string
}

// Collect exact keys before deleting anything. These are legacy projections,
// never the current Glivela business state. Unknown incoming references refuse
// the transaction instead of being erased through CASCADE or SET NULL.
func supersededHistoryScopes(ctx context.Context, tx *sql.Tx, oldID, endpointID, oldService string, credentials []string) ([]supersededHistoryScope, error) {
	specs := []struct {
		table, column, query string
		arg                  string
		frozen               bool
		allowed              map[string]bool
	}{
		{"application_adoptions", "id", `SELECT id FROM application_adoptions WHERE application_id=?`, oldID, false, nil},
		{"xray_configuration_recoveries", "id", `SELECT id FROM xray_configuration_recoveries WHERE application_id=?`, oldID, false, nil},
		{"three_x_ui_nodes", "worker_application_id", `SELECT worker_application_id FROM three_x_ui_nodes WHERE worker_application_id=?`, oldID, false, nil},
		{"landing_client_grants", "id", `SELECT id FROM landing_client_grants WHERE application_id=?`, oldID, true, nil},
		{"landing_proxy_states", "node_id", `SELECT node_id FROM landing_proxy_states WHERE application_id=?`, oldID, true, nil},
		{"meridian_route_health_events", "id", `SELECT id FROM meridian_route_health_events WHERE route_id IN(SELECT id FROM meridian_route_grants WHERE endpoint_id=?)`, endpointID, true, nil},
		{"meridian_route_grants", "id", `SELECT id FROM meridian_route_grants WHERE endpoint_id=?`, endpointID, true, map[string]bool{"meridian_route_health_events/route_id": true}},
		{"meridian_deployments", "endpoint_id", `SELECT endpoint_id FROM meridian_deployments WHERE endpoint_id=?`, endpointID, true, nil},
		{"glivela_pilot_checkpoint", "endpoint_id", `SELECT endpoint_id FROM glivela_pilot_checkpoint WHERE endpoint_id=?`, endpointID, true, nil},
	}
	var out []supersededHistoryScope
	for _, spec := range specs {
		ids, err := historyStrings(ctx, tx, spec.query, spec.arg)
		if err != nil {
			return nil, err
		}
		if err := requireOnlyHistoryReferences(ctx, tx, spec.table, ids, spec.allowed); err != nil {
			return nil, err
		}
		out = append(out, supersededHistoryScope{table: spec.table, column: spec.column, ids: ids, frozen: spec.frozen})
	}
	out = append(out,
		supersededHistoryScope{table: "meridian_line_usage", column: "credential_id", ids: credentials, frozen: true},
		supersededHistoryScope{table: "meridian_usage_watermarks", column: "credential_id", ids: credentials, frozen: true},
		supersededHistoryScope{table: "meridian_credentials", column: "id", ids: credentials, frozen: true},
		supersededHistoryScope{table: "meridian_endpoints", column: "id", ids: []string{endpointID}, frozen: true},
	)
	if oldService != "" {
		pubs, err := historyStrings(ctx, tx, `SELECT id FROM publications WHERE service_id=?`, oldService)
		if err != nil {
			return nil, err
		}
		if err := requireOnlyHistoryReferences(ctx, tx, "publications", pubs, map[string]bool{"reality_security_checks/publication_id": true}); err != nil {
			return nil, err
		}
		out = append(out, supersededHistoryScope{table: "reality_security_checks", column: "publication_id", ids: pubs})
		out = append(out, supersededHistoryScope{table: "publications", column: "id", ids: pubs})
		for _, table := range []string{"three_x_ui_reality_guards", "three_x_ui_inbound_plans", "three_x_ui_node_protocols"} {
			ids, err := historyStrings(ctx, tx, "SELECT service_id FROM "+table+" WHERE service_id=?", oldService)
			if err != nil {
				return nil, err
			}
			if err := requireOnlyHistoryReferences(ctx, tx, table, ids, nil); err != nil {
				return nil, err
			}
			out = append(out, supersededHistoryScope{table: table, column: "service_id", ids: ids})
		}
		out = append(out, supersededHistoryScope{table: "services", column: "id", ids: []string{oldService}})
	}
	return out, nil
}
