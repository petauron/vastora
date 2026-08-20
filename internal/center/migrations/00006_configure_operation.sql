-- +goose Up
CREATE TABLE deployments_v6 (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    app_key TEXT NOT NULL,
    app_version TEXT NOT NULL,
    manifest_json BLOB NOT NULL,
    config_json BLOB NOT NULL,
    secret_id TEXT REFERENCES secrets(id),
    operation TEXT NOT NULL CHECK(operation IN ('install', 'upgrade', 'configure', 'uninstall')),
    delete_data INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL CHECK(state IN ('pending', 'running', 'succeeded', 'failed')),
    attempt INTEGER NOT NULL DEFAULT 0,
    lease_expires_at TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE
);

INSERT INTO deployments_v6 (
    id, agent_id, app_key, app_version, manifest_json, config_json, secret_id,
    operation, delete_data, state, attempt, lease_expires_at, error,
    created_at, updated_at, application_id
)
SELECT
    id, agent_id, app_key, app_version, manifest_json, config_json, secret_id,
    operation, delete_data, state, attempt, lease_expires_at, error,
    created_at, updated_at, application_id
FROM deployments;

DROP TABLE deployments;
ALTER TABLE deployments_v6 RENAME TO deployments;
CREATE UNIQUE INDEX deployments_one_active_task_idx ON deployments(agent_id, app_key) WHERE state IN ('pending', 'running');
PRAGMA user_version = 6;
