-- +goose Up
CREATE TABLE three_x_ui_backups (
    application_id TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL CHECK(state IN ('pending', 'ready', 'failed')),
    sealed BLOB,
    sha256 TEXT NOT NULL DEFAULT '',
    size INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE three_x_ui_migrations (
    id TEXT PRIMARY KEY,
    site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    source_application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
    target_application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
    backup_revision INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL CHECK(state IN ('backing_up', 'restoring', 'switching', 'ready', 'failed')),
    step TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK(source_application_id <> target_application_id)
);
CREATE UNIQUE INDEX three_x_ui_migrations_one_active_idx
ON three_x_ui_migrations(site_id)
WHERE state IN ('backing_up', 'restoring', 'switching');

CREATE TABLE application_commands_v12 (
    id TEXT PRIMARY KEY,
    application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    gateway_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK(kind IN ('3xui.reality.create', '3xui.subscription.configure', '3xui.clients.manage', '3xui.node.reconcile', '3xui.controller.manage')),
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
INSERT INTO application_commands_v12 SELECT * FROM application_commands;
DROP TABLE application_commands;
ALTER TABLE application_commands_v12 RENAME TO application_commands;
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
CREATE TRIGGER deployments_block_during_three_x_ui_migration
BEFORE INSERT ON deployments
WHEN NEW.app_key = 'vastora-official/3x-ui' AND EXISTS (
    SELECT 1 FROM three_x_ui_migrations
    WHERE state IN ('backing_up', 'restoring', 'switching')
    AND (source_application_id = NEW.application_id OR target_application_id = NEW.application_id)
)
BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END;

PRAGMA user_version = 12;
