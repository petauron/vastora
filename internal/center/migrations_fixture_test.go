package center

import "testing"

// Older migration tests reconstruct a historical database from a fresh store.
// Newer ownership tables and cross-table triggers cannot be left in that input.
func removePostVersion85TablesForFixture(t *testing.T, store *Store) {
	t.Helper()
	for _, statement := range []string{
		`DROP TRIGGER application_commands_block_during_meridian_cutover`,
		`DROP TRIGGER application_command_updates_block_during_meridian_cutover`,
		`DROP TRIGGER deployments_block_during_meridian_cutover`,
		`DROP TRIGGER deployment_updates_block_during_meridian_cutover`,
		`DROP TABLE meridian_deployments`,
		`DROP TABLE meridian_subscription_snapshots`,
		`DROP TABLE meridian_usage_watermarks`,
		`DROP TABLE meridian_route_grants`,
		`DROP TABLE meridian_credentials`,
		`DROP TABLE meridian_accounts`,
		`DROP TABLE meridian_endpoints`,
		`DROP TABLE meridian_cutover`,
		`DROP TABLE xray_configuration_recoveries`,
	} {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}
