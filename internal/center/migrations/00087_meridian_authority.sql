-- +goose Up
-- Establish the native authority before any legacy controller export is
-- attempted. The migration runner creates a verified SQLite backup first.
-- Legacy tables remain read-only migration input until the explicit cutover
-- reaches complete; there is no subscription or deployment fallback to them.
DROP TRIGGER secret_deliveries_delete_with_application_command;
DROP TRIGGER application_commands_block_during_three_x_ui_migration;
DROP TRIGGER application_command_updates_block_during_three_x_ui_migration;
DROP TRIGGER deployments_block_during_three_x_ui_data_plane;
DROP TRIGGER application_commands_block_during_three_x_ui_deployment;
DROP TRIGGER application_command_updates_block_during_three_x_ui_deployment;

CREATE TABLE application_commands_v87 (
 id TEXT PRIMARY KEY,
 application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
 site_id TEXT NOT NULL DEFAULT '',
 display_name TEXT NOT NULL DEFAULT '' COLLATE NOCASE,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 gateway_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
 kind TEXT NOT NULL CHECK(kind IN ('meridian.runtime.apply', 'meridian.legacy.export', 'meridian.legacy.retire', 'meridian.subscription.publish', 'pulse.enrollment.create', '3xui.reality.create', '3xui.reality.verify', '3xui.reality.harden', '3xui.reality.rename', '3xui.reality.remove', '3xui.protocols.configure', '3xui.subscription.configure', '3xui.clients.manage', '3xui.node.reconcile', '3xui.controller.manage')),
 input_json BLOB NOT NULL,
 result_json BLOB NOT NULL DEFAULT '{}',
 result_secret_id TEXT REFERENCES secrets(id) ON DELETE SET NULL,
 state TEXT NOT NULL CHECK(state IN ('pending', 'running', 'succeeded', 'failed')),
 reconciliation_required INTEGER NOT NULL DEFAULT 0 CHECK(reconciliation_required IN (0, 1)),
 reconciliation_requested INTEGER NOT NULL DEFAULT 0 CHECK(reconciliation_requested IN (0, 1)),
 attempt INTEGER NOT NULL DEFAULT 0,
 lease_expires_at TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
INSERT INTO application_commands_v87(rowid,id,application_id,site_id,display_name,agent_id,gateway_node_id,kind,input_json,result_json,result_secret_id,state,reconciliation_required,reconciliation_requested,attempt,lease_expires_at,error,created_at,updated_at)
SELECT rowid,id,application_id,site_id,display_name,agent_id,gateway_node_id,kind,input_json,result_json,result_secret_id,state,reconciliation_required,reconciliation_requested,attempt,lease_expires_at,error,created_at,updated_at FROM application_commands;
DROP TABLE application_commands;
ALTER TABLE application_commands_v87 RENAME TO application_commands;

-- +goose StatementBegin
CREATE TRIGGER secret_deliveries_delete_with_application_command AFTER DELETE ON application_commands
BEGIN DELETE FROM secret_deliveries WHERE kind = 'application_command_result' AND resource_id = OLD.id; END;
-- +goose StatementEnd

CREATE UNIQUE INDEX application_commands_one_active_idx ON application_commands(agent_id)
WHERE (state IN ('pending', 'running') OR reconciliation_required = 1) AND kind NOT IN ('3xui.controller.manage', 'pulse.enrollment.create');
CREATE UNIQUE INDEX pulse_enrollment_deployment_idx ON application_commands(json_extract(input_json, '$.deploymentId'))
WHERE kind = 'pulse.enrollment.create' AND state IN ('pending','running');
CREATE UNIQUE INDEX application_commands_one_active_controller_idx ON application_commands(application_id)
WHERE (state IN ('pending', 'running') OR reconciliation_required = 1) AND kind = '3xui.controller.manage';
CREATE UNIQUE INDEX application_commands_one_active_reality_name_idx ON application_commands(site_id, display_name COLLATE NOCASE)
WHERE site_id <> '' AND display_name <> '' AND kind IN ('3xui.reality.create', '3xui.reality.rename')
AND (state IN ('pending', 'running') OR reconciliation_required = 1);

-- +goose StatementBegin
CREATE TRIGGER application_commands_block_during_three_x_ui_migration
BEFORE INSERT ON application_commands
WHEN NEW.kind NOT IN ('3xui.controller.manage', 'pulse.enrollment.create')
AND NOT (NEW.kind = '3xui.node.reconcile' AND EXISTS (
 SELECT 1 FROM three_x_ui_migrations
 WHERE id = json_extract(CASE WHEN json_valid(NEW.input_json) THEN NEW.input_json ELSE '{}' END, '$.migrationId') AND state = 'switching'
))
AND EXISTS (SELECT 1 FROM three_x_ui_migrations WHERE state IN ('backing_up', 'restoring', 'switching'))
BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER application_command_updates_block_during_three_x_ui_migration
BEFORE UPDATE OF application_id, kind, input_json, state, reconciliation_required ON application_commands
WHEN NEW.kind NOT IN ('3xui.controller.manage', 'pulse.enrollment.create') AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1)
AND NOT (NEW.kind = '3xui.node.reconcile' AND EXISTS (
 SELECT 1 FROM three_x_ui_migrations
 WHERE id = json_extract(CASE WHEN json_valid(NEW.input_json) THEN NEW.input_json ELSE '{}' END, '$.migrationId') AND state = 'switching'
))
AND EXISTS (SELECT 1 FROM three_x_ui_migrations WHERE state IN ('backing_up', 'restoring', 'switching'))
BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER deployments_block_during_three_x_ui_data_plane
BEFORE INSERT ON deployments
WHEN NEW.app_key = 'vastora-official/3x-ui'
AND NOT EXISTS (
 SELECT 1 FROM three_x_ui_migrations
 WHERE source_application_id=NEW.application_id AND state='switching'
 AND step='convert_worker' AND last_error=''
)
AND EXISTS (
 SELECT 1 FROM application_commands command
 JOIN applications command_app ON command_app.id = command.application_id
 WHERE (command.state IN ('pending', 'running') OR command.reconciliation_required = 1)
 AND command.kind <> '3xui.controller.manage'
 AND command_app.app_key = 'vastora-official/3x-ui'
)
BEGIN SELECT RAISE(ABORT, '3x-ui data-plane operation is in progress'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER application_commands_block_during_three_x_ui_deployment
BEFORE INSERT ON application_commands
WHEN NEW.kind NOT IN ('3xui.controller.manage', '3xui.node.reconcile', 'pulse.enrollment.create')
AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1)
AND EXISTS (
 SELECT 1 FROM deployments deployment
 WHERE deployment.app_key = 'vastora-official/3x-ui'
 AND (deployment.state IN ('pending', 'running') OR deployment.reconciliation_required = 1)
)
BEGIN SELECT RAISE(ABORT, '3x-ui deployment is in progress'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER application_command_updates_block_during_three_x_ui_deployment
BEFORE UPDATE OF application_id, kind, input_json, state, reconciliation_required ON application_commands
WHEN NEW.kind NOT IN ('3xui.controller.manage', '3xui.node.reconcile', 'pulse.enrollment.create')
AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1)
AND EXISTS (
 SELECT 1 FROM deployments deployment
 WHERE deployment.app_key = 'vastora-official/3x-ui'
 AND (deployment.state IN ('pending', 'running') OR deployment.reconciliation_required = 1)
)
BEGIN SELECT RAISE(ABORT, '3x-ui deployment is in progress'); END;
-- +goose StatementEnd

CREATE TABLE meridian_cutover (
 id INTEGER PRIMARY KEY CHECK(id=1),
 state TEXT NOT NULL CHECK(state IN ('not_required','inspect','backup','import','publish','project','verify','retire','complete','failed')),
 legacy_controller_application_id TEXT REFERENCES applications(id) ON DELETE RESTRICT,
 backup_revision INTEGER NOT NULL DEFAULT 0 CHECK(backup_revision>=0),
 import_secret_id TEXT REFERENCES secrets(id) ON DELETE RESTRICT,
 import_sha256 TEXT NOT NULL DEFAULT '' CHECK(import_sha256='' OR (length(import_sha256)=64 AND import_sha256 NOT GLOB '*[^0-9a-f]*')),
 subscription_authority TEXT NOT NULL CHECK(subscription_authority IN ('legacy','meridian')),
 expected_accounts INTEGER NOT NULL DEFAULT 0 CHECK(expected_accounts>=0),
 expected_credentials INTEGER NOT NULL DEFAULT 0 CHECK(expected_credentials>=0),
 expected_endpoints INTEGER NOT NULL DEFAULT 0 CHECK(expected_endpoints>=0),
 expected_routes INTEGER NOT NULL DEFAULT 0 CHECK(expected_routes>=0),
 last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 switched_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE meridian_endpoints (
 id TEXT PRIMARY KEY,
 application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
 service_id TEXT NOT NULL REFERENCES services(id) ON DELETE RESTRICT,
 inbound_tag TEXT NOT NULL UNIQUE,
 listen_port INTEGER NOT NULL CHECK(listen_port BETWEEN 1 AND 65535),
 advertise_host TEXT NOT NULL,
 advertise_port INTEGER NOT NULL CHECK(advertise_port BETWEEN 1 AND 65535),
 target TEXT NOT NULL,
 target_ip TEXT NOT NULL,
 server_names_json BLOB NOT NULL CHECK(json_valid(server_names_json) AND json_type(server_names_json)='array'),
 private_key_secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
 public_key TEXT NOT NULL,
 short_ids_json BLOB NOT NULL CHECK(json_valid(short_ids_json) AND json_type(short_ids_json)='array'),
 fingerprint TEXT NOT NULL DEFAULT 'chrome',
 vless_enabled INTEGER NOT NULL DEFAULT 1 CHECK(vless_enabled IN(0,1)),
 hy2_enabled INTEGER NOT NULL DEFAULT 0 CHECK(hy2_enabled IN(0,1)),
 hy2_inbound_tag TEXT NOT NULL DEFAULT '',
 hy2_server_name TEXT NOT NULL DEFAULT '',
 hy2_certificate_secret_id TEXT REFERENCES secrets(id) ON DELETE RESTRICT,
 hy2_private_key_secret_id TEXT REFERENCES secrets(id) ON DELETE RESTRICT,
 hy2_certificate_not_after TEXT NOT NULL DEFAULT '',
 desired_revision INTEGER NOT NULL DEFAULT 1 CHECK(desired_revision>0),
 applied_revision INTEGER NOT NULL DEFAULT 0 CHECK(applied_revision BETWEEN 0 AND desired_revision),
 runtime_healthy INTEGER NOT NULL DEFAULT 0 CHECK(runtime_healthy IN(0,1)),
 legacy_retired INTEGER NOT NULL DEFAULT 0 CHECK(legacy_retired IN(0,1)),
 status TEXT NOT NULL CHECK(status IN ('importing','pending','applying','ready','failed','retired')),
 last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(application_id),
 UNIQUE(service_id),
 CHECK(vless_enabled=1 OR hy2_enabled=1),
 CHECK(hy2_enabled=0 OR (hy2_inbound_tag<>'' AND hy2_server_name<>'' AND hy2_certificate_secret_id IS NOT NULL AND hy2_private_key_secret_id IS NOT NULL))
);
CREATE UNIQUE INDEX meridian_endpoints_hy2_tag ON meridian_endpoints(hy2_inbound_tag) WHERE hy2_inbound_tag<>'';
CREATE TABLE meridian_accounts (
 id TEXT PRIMARY KEY,
 display_name TEXT NOT NULL,
 total_bytes INTEGER NOT NULL DEFAULT 0 CHECK(total_bytes>=0),
 expiry_time INTEGER NOT NULL DEFAULT 0 CHECK(expiry_time>=0),
 reset_days INTEGER NOT NULL DEFAULT 0 CHECK(reset_days BETWEEN 0 AND 3650),
 next_reset_at TEXT NOT NULL DEFAULT '',
 last_reset_at TEXT NOT NULL DEFAULT '',
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN(0,1)),
 subscription_token_secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
 subscription_token_sha256 TEXT NOT NULL UNIQUE CHECK(length(subscription_token_sha256)=64 AND subscription_token_sha256 NOT GLOB '*[^0-9a-f]*'),
 desired_revision INTEGER NOT NULL DEFAULT 1 CHECK(desired_revision>0),
 applied_revision INTEGER NOT NULL DEFAULT 0 CHECK(applied_revision BETWEEN 0 AND desired_revision),
 status TEXT NOT NULL CHECK(status IN ('importing','active','disabled','expired','failed')),
 last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE TABLE meridian_credentials (
 id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES meridian_accounts(id) ON DELETE RESTRICT,
 endpoint_id TEXT NOT NULL REFERENCES meridian_endpoints(id) ON DELETE RESTRICT,
 kind TEXT NOT NULL CHECK(kind IN ('native','route')),
 user_name TEXT NOT NULL UNIQUE,
 identity_sha256 TEXT NOT NULL CHECK(length(identity_sha256)=64 AND identity_sha256 NOT GLOB '*[^0-9a-f]*'),
 protocol_secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
 hy2_auth_secret_id TEXT REFERENCES secrets(id) ON DELETE RESTRICT,
 hy2_identity_sha256 TEXT NOT NULL DEFAULT '' CHECK(hy2_identity_sha256='' OR (length(hy2_identity_sha256)=64 AND hy2_identity_sha256 NOT GLOB '*[^0-9a-f]*')),
 egress_node_id TEXT REFERENCES agents(id) ON DELETE RESTRICT,
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN(0,1)),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 CHECK((kind='native' AND egress_node_id IS NULL) OR (kind='route' AND egress_node_id IS NOT NULL)),
 CHECK(kind='native' OR (hy2_auth_secret_id IS NULL AND hy2_identity_sha256='')),
 CHECK((hy2_auth_secret_id IS NULL AND hy2_identity_sha256='') OR (hy2_auth_secret_id IS NOT NULL AND hy2_identity_sha256<>''))
);
CREATE INDEX meridian_credentials_account ON meridian_credentials(account_id,kind,enabled);
CREATE INDEX meridian_credentials_endpoint ON meridian_credentials(endpoint_id,enabled);
CREATE UNIQUE INDEX meridian_credentials_endpoint_identity ON meridian_credentials(endpoint_id,identity_sha256);
CREATE UNIQUE INDEX meridian_credentials_endpoint_hy2_identity ON meridian_credentials(endpoint_id,hy2_identity_sha256) WHERE hy2_identity_sha256<>'';
CREATE TABLE meridian_route_grants (
 id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES meridian_accounts(id) ON DELETE RESTRICT,
 endpoint_id TEXT NOT NULL REFERENCES meridian_endpoints(id) ON DELETE RESTRICT,
 egress_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
 base_credential_id TEXT NOT NULL REFERENCES meridian_credentials(id) ON DELETE RESTRICT,
 route_credential_id TEXT NOT NULL UNIQUE REFERENCES meridian_credentials(id) ON DELETE RESTRICT,
 mode TEXT NOT NULL DEFAULT 'fixed' CHECK(mode='fixed'),
 hide_native INTEGER NOT NULL DEFAULT 0 CHECK(hide_native IN(0,1)),
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN(0,1)),
 desired_revision INTEGER NOT NULL DEFAULT 1 CHECK(desired_revision>0),
 applied_revision INTEGER NOT NULL DEFAULT 0 CHECK(applied_revision BETWEEN 0 AND desired_revision),
 runtime_healthy INTEGER NOT NULL DEFAULT 0 CHECK(runtime_healthy IN(0,1)),
 status TEXT NOT NULL CHECK(status IN ('importing','pending','applying','ready','blocked','revoking','revoked','failed')),
 last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 CHECK(base_credential_id<>route_credential_id),
 UNIQUE(account_id,endpoint_id,egress_node_id)
);
CREATE INDEX meridian_route_grants_endpoint ON meridian_route_grants(endpoint_id,status);
CREATE TABLE meridian_usage_watermarks (
 credential_id TEXT PRIMARY KEY REFERENCES meridian_credentials(id) ON DELETE CASCADE,
 baseline_bytes INTEGER NOT NULL DEFAULT 0 CHECK(baseline_bytes>=0),
 observed_bytes INTEGER NOT NULL DEFAULT 0 CHECK(observed_bytes>=baseline_bytes),
 raw_up_bytes INTEGER NOT NULL DEFAULT 0 CHECK(raw_up_bytes>=0),
 raw_down_bytes INTEGER NOT NULL DEFAULT 0 CHECK(raw_down_bytes>=0),
 observed_at TEXT NOT NULL
);
CREATE TABLE meridian_subscription_snapshots (
 account_id TEXT PRIMARY KEY REFERENCES meridian_accounts(id) ON DELETE CASCADE,
 secret_id TEXT NOT NULL UNIQUE REFERENCES secrets(id) ON DELETE RESTRICT,
 content_sha256 TEXT NOT NULL CHECK(length(content_sha256)=64 AND content_sha256 NOT GLOB '*[^0-9a-f]*'),
 account_revision INTEGER NOT NULL CHECK(account_revision>0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
-- +goose StatementBegin
CREATE TRIGGER meridian_subscription_snapshots_delete_secret
AFTER DELETE ON meridian_subscription_snapshots
BEGIN DELETE FROM secrets WHERE id=OLD.secret_id; END;
-- +goose StatementEnd
CREATE INDEX meridian_accounts_reset ON meridian_accounts(next_reset_at) WHERE reset_days>0 AND status='active';
CREATE TABLE meridian_deployments (
 endpoint_id TEXT PRIMARY KEY REFERENCES meridian_endpoints(id) ON DELETE CASCADE,
 desired_revision INTEGER NOT NULL CHECK(desired_revision>0),
 applied_revision INTEGER NOT NULL DEFAULT 0 CHECK(applied_revision BETWEEN 0 AND desired_revision),
 desired_sha256 TEXT NOT NULL CHECK(length(desired_sha256)=64 AND desired_sha256 NOT GLOB '*[^0-9a-f]*'),
 applied_sha256 TEXT NOT NULL DEFAULT '' CHECK(applied_sha256='' OR (length(applied_sha256)=64 AND applied_sha256 NOT GLOB '*[^0-9a-f]*')),
 command_id TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL CHECK(status IN ('pending','applying','ready','failed')),
 last_error TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL
);
CREATE INDEX meridian_deployments_status ON meridian_deployments(status,updated_at);

INSERT INTO meridian_cutover(id,state,legacy_controller_application_id,subscription_authority,created_at,updated_at)
SELECT 1,
 CASE WHEN controller_application_id IS NULL THEN 'not_required' ELSE 'inspect' END,
 controller_application_id,
 CASE WHEN controller_application_id IS NULL THEN 'meridian' ELSE 'legacy' END,
 strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now')
FROM three_x_ui_control_plane WHERE id=1;

INSERT INTO meridian_cutover(id,state,subscription_authority,created_at,updated_at)
SELECT 1,'not_required','meridian',strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now')
WHERE NOT EXISTS(SELECT 1 FROM meridian_cutover WHERE id=1);

-- Once export starts, the inspected legacy graph is immutable. The backup
-- command was inserted while the state was inspect (or failed) and may finish
-- normally, while every later cutover task uses a Meridian command or package.
-- New legacy writes would otherwise race the exported snapshot or recreate
-- obsolete authority after applications have changed identity.
-- +goose StatementBegin
CREATE TRIGGER application_commands_block_during_meridian_cutover
BEFORE INSERT ON application_commands
WHEN NEW.kind GLOB '3xui.*'
AND EXISTS(SELECT 1 FROM meridian_cutover WHERE id=1 AND state IN ('backup','import','publish','project','verify','retire'))
BEGIN SELECT RAISE(ABORT, 'Meridian authority cutover is in progress'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER application_command_updates_block_during_meridian_cutover
BEFORE UPDATE OF application_id,kind,input_json,state,reconciliation_required ON application_commands
WHEN NEW.kind GLOB '3xui.*'
AND (NEW.state IN ('pending','running') OR NEW.reconciliation_required=1)
AND EXISTS(SELECT 1 FROM meridian_cutover WHERE id=1 AND state IN ('backup','import','publish','project','verify','retire'))
BEGIN SELECT RAISE(ABORT, 'Meridian authority cutover is in progress'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER deployments_block_during_meridian_cutover
BEFORE INSERT ON deployments
WHEN NEW.app_key='vastora-official/3x-ui'
AND EXISTS(SELECT 1 FROM meridian_cutover WHERE id=1 AND state IN ('backup','import','publish','project','verify','retire'))
BEGIN SELECT RAISE(ABORT, 'Meridian authority cutover is in progress'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER deployment_updates_block_during_meridian_cutover
BEFORE UPDATE OF application_id,app_key,state,reconciliation_required ON deployments
WHEN NEW.app_key='vastora-official/3x-ui'
AND (NEW.state IN ('pending','running') OR NEW.reconciliation_required=1)
AND EXISTS(SELECT 1 FROM meridian_cutover WHERE id=1 AND state IN ('backup','import','publish','project','verify','retire'))
BEGIN SELECT RAISE(ABORT, 'Meridian authority cutover is in progress'); END;
-- +goose StatementEnd

UPDATE settings SET key='meridian_landing_selection'
WHERE key='three_x_ui_landing_selection'
  AND NOT EXISTS(SELECT 1 FROM settings WHERE key='meridian_landing_selection');
DELETE FROM settings WHERE key='three_x_ui_landing_selection';

PRAGMA user_version = 87;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
