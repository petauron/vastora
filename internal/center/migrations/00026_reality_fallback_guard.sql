-- +goose Up
DROP TRIGGER application_commands_block_during_three_x_ui_migration;
DROP TRIGGER application_command_updates_block_during_three_x_ui_migration;
DROP TRIGGER deployments_block_during_three_x_ui_data_plane;
DROP TRIGGER application_commands_block_during_three_x_ui_deployment;
DROP TRIGGER application_command_updates_block_during_three_x_ui_deployment;

DROP INDEX application_commands_one_active_idx;
DROP INDEX application_commands_one_active_controller_idx;
DROP INDEX application_commands_one_active_reality_name_idx;

ALTER TABLE application_commands RENAME TO application_commands_v25;

CREATE TABLE application_commands (
    id TEXT PRIMARY KEY,
    application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    site_id TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '' COLLATE NOCASE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    gateway_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK(kind IN (
        '3xui.reality.create',
        '3xui.reality.verify',
        '3xui.reality.harden',
        '3xui.reality.rename',
        '3xui.subscription.configure',
        '3xui.clients.manage',
        '3xui.node.reconcile',
        '3xui.controller.manage'
    )),
    input_json BLOB NOT NULL,
    result_json BLOB NOT NULL DEFAULT '{}',
    result_secret_id TEXT REFERENCES secrets(id) ON DELETE SET NULL,
    state TEXT NOT NULL CHECK(state IN ('pending', 'running', 'succeeded', 'failed')),
    reconciliation_required INTEGER NOT NULL DEFAULT 0 CHECK(reconciliation_required IN (0, 1)),
    attempt INTEGER NOT NULL DEFAULT 0,
    lease_expires_at TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT INTO application_commands(
    id, application_id, site_id, display_name, agent_id, gateway_node_id,
    kind, input_json, result_json, result_secret_id, state,
    reconciliation_required, attempt, lease_expires_at, error, created_at, updated_at
)
SELECT
    id, application_id, site_id, display_name, agent_id, gateway_node_id,
    kind, input_json, result_json, result_secret_id, state,
    reconciliation_required, attempt, lease_expires_at, error, created_at, updated_at
FROM application_commands_v25;

DROP TABLE application_commands_v25;

CREATE UNIQUE INDEX application_commands_one_active_idx
ON application_commands(agent_id)
WHERE (state IN ('pending', 'running') OR reconciliation_required = 1)
  AND kind <> '3xui.controller.manage';

CREATE UNIQUE INDEX application_commands_one_active_controller_idx
ON application_commands(application_id)
WHERE (state IN ('pending', 'running') OR reconciliation_required = 1)
  AND kind = '3xui.controller.manage';

CREATE UNIQUE INDEX application_commands_one_active_reality_name_idx
ON application_commands(site_id, display_name COLLATE NOCASE)
WHERE site_id <> '' AND display_name <> ''
  AND kind IN ('3xui.reality.create', '3xui.reality.rename')
  AND (state IN ('pending', 'running') OR reconciliation_required = 1);

CREATE TRIGGER application_commands_block_during_three_x_ui_migration
BEFORE INSERT ON application_commands
WHEN NEW.kind <> '3xui.controller.manage'
AND NOT (NEW.kind = '3xui.node.reconcile' AND EXISTS (
    SELECT 1 FROM three_x_ui_migrations
    WHERE id = json_extract(CASE WHEN json_valid(NEW.input_json) THEN NEW.input_json ELSE '{}' END, '$.migrationId')
      AND state = 'switching'
))
AND EXISTS (
    SELECT 1 FROM three_x_ui_migrations migration
    JOIN applications queued ON queued.id = NEW.application_id
    WHERE migration.state IN ('backing_up', 'restoring', 'switching')
      AND migration.site_id = queued.site_id
)
BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END;

CREATE TRIGGER application_command_updates_block_during_three_x_ui_migration
BEFORE UPDATE OF application_id, kind, input_json, state ON application_commands
WHEN NEW.kind <> '3xui.controller.manage'
AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1)
AND NOT (NEW.kind = '3xui.node.reconcile' AND EXISTS (
    SELECT 1 FROM three_x_ui_migrations
    WHERE id = json_extract(CASE WHEN json_valid(NEW.input_json) THEN NEW.input_json ELSE '{}' END, '$.migrationId')
      AND state = 'switching'
))
AND EXISTS (
    SELECT 1 FROM three_x_ui_migrations migration
    JOIN applications queued ON queued.id = NEW.application_id
    WHERE migration.state IN ('backing_up', 'restoring', 'switching')
      AND migration.site_id = queued.site_id
)
BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END;

CREATE TRIGGER deployments_block_during_three_x_ui_data_plane
BEFORE INSERT ON deployments
WHEN NEW.app_key = 'vastora-official/3x-ui' AND EXISTS (
    SELECT 1 FROM application_commands command
    JOIN applications command_app ON command_app.id = command.application_id
    JOIN applications deployment_app ON deployment_app.id = NEW.application_id
    WHERE (command.state IN ('pending', 'running') OR command.reconciliation_required = 1)
      AND command.kind <> '3xui.controller.manage'
      AND command_app.site_id = deployment_app.site_id
)
BEGIN SELECT RAISE(ABORT, '3x-ui data-plane operation is in progress'); END;

CREATE TRIGGER application_commands_block_during_three_x_ui_deployment
BEFORE INSERT ON application_commands
WHEN NEW.kind NOT IN ('3xui.controller.manage', '3xui.node.reconcile')
AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1)
AND EXISTS (
    SELECT 1 FROM deployments deployment
    JOIN applications deployment_app ON deployment_app.id = deployment.application_id
    JOIN applications command_app ON command_app.id = NEW.application_id
    WHERE deployment.app_key = 'vastora-official/3x-ui'
      AND (deployment.state IN ('pending', 'running') OR deployment.reconciliation_required = 1)
      AND deployment_app.site_id = command_app.site_id
)
BEGIN SELECT RAISE(ABORT, '3x-ui deployment is in progress'); END;

CREATE TRIGGER application_command_updates_block_during_three_x_ui_deployment
BEFORE UPDATE OF application_id, kind, input_json, state, reconciliation_required ON application_commands
WHEN NEW.kind NOT IN ('3xui.controller.manage', '3xui.node.reconcile')
AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1)
AND EXISTS (
    SELECT 1 FROM deployments deployment
    JOIN applications deployment_app ON deployment_app.id = deployment.application_id
    JOIN applications command_app ON command_app.id = NEW.application_id
    WHERE deployment.app_key = 'vastora-official/3x-ui'
      AND (deployment.state IN ('pending', 'running') OR deployment.reconciliation_required = 1)
      AND deployment_app.site_id = command_app.site_id
)
BEGIN SELECT RAISE(ABORT, '3x-ui deployment is in progress'); END;

CREATE TABLE IF NOT EXISTS three_x_ui_reality_guards (
    service_id TEXT PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    target_host TEXT NOT NULL,
    target_ip TEXT NOT NULL,
    server_name TEXT NOT NULL,
    node_asn INTEGER NOT NULL DEFAULT 0 CHECK(node_asn >= 0),
    target_asn INTEGER NOT NULL DEFAULT 0 CHECK(target_asn >= 0),
    cdn_provider TEXT NOT NULL DEFAULT '',
    companion_inbound_id INTEGER NOT NULL DEFAULT 0 CHECK(companion_inbound_id >= 0),
    companion_tag TEXT NOT NULL,
    companion_port INTEGER NOT NULL DEFAULT 0 CHECK(companion_port = 0 OR companion_port BETWEEN 21000 AND 21031),
    revision INTEGER NOT NULL DEFAULT 1 CHECK(revision > 0),
    status TEXT NOT NULL CHECK(status IN ('pending', 'hardening', 'ready', 'action_required')),
    verified_at TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS three_x_ui_reality_guards_status_idx
ON three_x_ui_reality_guards(status, updated_at);

INSERT INTO three_x_ui_reality_guards(
    service_id, target_host, target_ip, server_name, companion_tag,
    status, last_error, created_at, updated_at
)
SELECT
    service.id,
    CASE
        WHEN substr(COALESCE(json_extract(command.result_json, '$.target'), ''), -4) = ':443'
        THEN substr(json_extract(command.result_json, '$.target'), 1, length(json_extract(command.result_json, '$.target')) - 4)
        ELSE COALESCE(json_extract(command.result_json, '$.target'), '')
    END,
    '',
    COALESCE(json_extract(command.result_json, '$.sniHostname'), ''),
    'vastora-guard-pending-' || service.id,
    'action_required',
    'REALITY inbound must be disabled and hardened before publication',
    service.created_at,
    service.updated_at
FROM services service
LEFT JOIN application_commands command
  ON command.application_id = service.application_id
 AND command.kind = '3xui.reality.create'
 AND command.state = 'succeeded'
 AND CAST(json_extract(command.result_json, '$.inboundId') AS INTEGER) = CAST(substr(service.name, 9) AS INTEGER)
WHERE service.app_protocol = 'vless/tcp/reality'
  AND service.status <> 'stopped';

UPDATE services
SET status = 'degraded',
    last_error = 'REALITY guard requires hardening before publication'
WHERE app_protocol = 'vless/tcp/reality'
  AND status <> 'stopped';

DELETE FROM routes
WHERE publication_id IN (
    SELECT publication.id
    FROM publications publication
    JOIN services service ON service.id = publication.service_id
    WHERE service.app_protocol = 'vless/tcp/reality'
      AND publication.status <> 'stopped'
);

UPDATE publications
SET status = 'stopped',
    last_error = 'REALITY guard requires hardening before publication',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE service_id IN (
    SELECT id FROM services WHERE app_protocol = 'vless/tcp/reality'
)
AND status <> 'stopped';

PRAGMA user_version = 26;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
