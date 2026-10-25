-- +goose Up
CREATE TABLE retired_proxy_business (id INTEGER PRIMARY KEY CHECK(id=1),proof_json BLOB NOT NULL CHECK(json_valid(proof_json)));
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_cutover_insert BEFORE INSERT ON meridian_cutover WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_cutover_update BEFORE UPDATE ON meridian_cutover WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_cutover_delete BEFORE DELETE ON meridian_cutover WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_endpoints_insert BEFORE INSERT ON meridian_endpoints WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_endpoints_update BEFORE UPDATE ON meridian_endpoints WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_endpoints_delete BEFORE DELETE ON meridian_endpoints WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_accounts_insert BEFORE INSERT ON meridian_accounts WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_accounts_update BEFORE UPDATE ON meridian_accounts WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_accounts_delete BEFORE DELETE ON meridian_accounts WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_credentials_insert BEFORE INSERT ON meridian_credentials WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_credentials_update BEFORE UPDATE ON meridian_credentials WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_credentials_delete BEFORE DELETE ON meridian_credentials WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_route_grants_insert BEFORE INSERT ON meridian_route_grants WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_route_grants_update BEFORE UPDATE ON meridian_route_grants WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_route_grants_delete BEFORE DELETE ON meridian_route_grants WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_line_usage_insert BEFORE INSERT ON meridian_line_usage WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_line_usage_update BEFORE UPDATE ON meridian_line_usage WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_line_usage_delete BEFORE DELETE ON meridian_line_usage WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_usage_watermarks_insert BEFORE INSERT ON meridian_usage_watermarks WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_usage_watermarks_update BEFORE UPDATE ON meridian_usage_watermarks WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_usage_watermarks_delete BEFORE DELETE ON meridian_usage_watermarks WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_subscription_snapshots_insert BEFORE INSERT ON meridian_subscription_snapshots WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_subscription_snapshots_update BEFORE UPDATE ON meridian_subscription_snapshots WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_subscription_snapshots_delete BEFORE DELETE ON meridian_subscription_snapshots WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_deployments_insert BEFORE INSERT ON meridian_deployments WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_deployments_update BEFORE UPDATE ON meridian_deployments WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_deployments_delete BEFORE DELETE ON meridian_deployments WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_route_health_events_insert BEFORE INSERT ON meridian_route_health_events WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_route_health_events_update BEFORE UPDATE ON meridian_route_health_events WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_meridian_route_health_events_delete BEFORE DELETE ON meridian_route_health_events WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_landing_server_states_insert BEFORE INSERT ON landing_server_states WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_landing_server_states_update BEFORE UPDATE ON landing_server_states WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_landing_server_states_delete BEFORE DELETE ON landing_server_states WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_landing_proxy_states_insert BEFORE INSERT ON landing_proxy_states WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_landing_proxy_states_update BEFORE UPDATE ON landing_proxy_states WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_landing_proxy_states_delete BEFORE DELETE ON landing_proxy_states WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_landing_proxy_retirements_insert BEFORE INSERT ON landing_proxy_retirements WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_landing_proxy_retirements_update BEFORE UPDATE ON landing_proxy_retirements WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_landing_proxy_retirements_delete BEFORE DELETE ON landing_proxy_retirements WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_landing_client_grants_insert BEFORE INSERT ON landing_client_grants WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_landing_client_grants_update BEFORE UPDATE ON landing_client_grants WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_landing_client_grants_delete BEFORE DELETE ON landing_client_grants WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_landing_client_blocks_insert BEFORE INSERT ON landing_client_blocks WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_landing_client_blocks_update BEFORE UPDATE ON landing_client_blocks WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_landing_client_blocks_delete BEFORE DELETE ON landing_client_blocks WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_node_egress_policies_insert BEFORE INSERT ON node_egress_policies WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_node_egress_policies_update BEFORE UPDATE ON node_egress_policies WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_node_egress_policies_delete BEFORE DELETE ON node_egress_policies WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_glivela_pilot_checkpoint_insert BEFORE INSERT ON glivela_pilot_checkpoint WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_glivela_pilot_checkpoint_update BEFORE UPDATE ON glivela_pilot_checkpoint WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER retired_proxy_glivela_pilot_checkpoint_delete BEFORE DELETE ON glivela_pilot_checkpoint WHEN EXISTS(SELECT 1 FROM retired_proxy_business WHERE id=1) BEGIN SELECT RAISE(ABORT,'proxy business permanently handed over to Glivela'); END;
-- +goose StatementEnd
PRAGMA user_version=121;

-- +goose Down
SELECT RAISE(ABORT,'database downgrade unsupported');
