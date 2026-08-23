-- +goose Up
ALTER TABLE services ADD COLUMN display_name TEXT NOT NULL DEFAULT '';

CREATE TABLE application_commands_v13 (
    id TEXT PRIMARY KEY,
    application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    gateway_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK(kind IN ('3xui.reality.create', '3xui.reality.rename', '3xui.subscription.configure', '3xui.clients.manage', '3xui.node.reconcile', '3xui.controller.manage')),
    input_json BLOB NOT NULL,
    result_json BLOB NOT NULL DEFAULT '{}',
    result_secret_id TEXT REFERENCES secrets(id) ON DELETE SET NULL,
    state TEXT NOT NULL CHECK(state IN ('pending', 'running', 'succeeded', 'failed')),
    attempt INTEGER NOT NULL DEFAULT 0,
    lease_expires_at TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
INSERT INTO application_commands_v13 SELECT * FROM application_commands;
DROP TABLE application_commands;
ALTER TABLE application_commands_v13 RENAME TO application_commands;

UPDATE application_commands
SET input_json = json_remove(json_set(
        input_json,
        '$.action', 'create',
        '$.displayName', COALESCE(json_extract(input_json, '$.name'), 'VLESS node'),
        '$.clientName', COALESCE(json_extract(input_json, '$.name'), 'My device')
    ), '$.name')
WHERE kind = '3xui.reality.create';

UPDATE application_commands
SET result_json = json_remove(json_set(
        result_json,
        '$.action', 'create',
        '$.displayName', COALESCE(json_extract(result_json, '$.name'), json_extract(input_json, '$.displayName')),
        '$.clientName', COALESCE(json_extract(result_json, '$.name'), json_extract(input_json, '$.clientName'))
    ), '$.name')
WHERE kind = '3xui.reality.create' AND json_valid(result_json);

CREATE UNIQUE INDEX application_commands_one_active_idx ON application_commands(application_id) WHERE state IN ('pending', 'running') AND kind <> '3xui.controller.manage';
CREATE UNIQUE INDEX application_commands_one_active_controller_idx ON application_commands(application_id) WHERE state IN ('pending', 'running') AND kind = '3xui.controller.manage';
CREATE TRIGGER application_commands_block_during_three_x_ui_migration
BEFORE INSERT ON application_commands
WHEN NEW.kind <> '3xui.controller.manage' AND EXISTS (
    SELECT 1 FROM three_x_ui_migrations
    WHERE state IN ('backing_up', 'restoring', 'switching')
    AND (source_application_id = NEW.application_id OR target_application_id = NEW.application_id)
)
BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END;

PRAGMA user_version = 13;
