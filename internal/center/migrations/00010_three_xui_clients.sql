-- +goose Up
CREATE TABLE application_commands_v10 (
    id TEXT PRIMARY KEY,
    application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    gateway_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK(kind IN ('3xui.reality.create', '3xui.subscription.configure', '3xui.clients.manage')),
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
INSERT INTO application_commands_v10 SELECT * FROM application_commands;
DROP TABLE application_commands;
ALTER TABLE application_commands_v10 RENAME TO application_commands;
CREATE UNIQUE INDEX application_commands_one_active_idx ON application_commands(application_id) WHERE state IN ('pending', 'running');

PRAGMA user_version = 10;
