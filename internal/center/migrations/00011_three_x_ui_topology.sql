-- +goose Up
ALTER TABLE applications ADD COLUMN role TEXT NOT NULL DEFAULT '' CHECK(role IN ('', 'master', 'worker'));

UPDATE applications
SET role = 'worker'
WHERE app_key = 'vastora-official/3x-ui';

UPDATE applications AS candidate
SET role = 'master'
WHERE candidate.app_key = 'vastora-official/3x-ui'
  AND NOT EXISTS (
    SELECT 1
    FROM applications AS preferred
    WHERE preferred.app_key = candidate.app_key
      AND preferred.site_id = candidate.site_id
      AND (
        CASE preferred.status WHEN 'running' THEN 0 WHEN 'deploying' THEN 1 WHEN 'pending' THEN 1 WHEN 'failed' THEN 2 ELSE 3 END < CASE candidate.status WHEN 'running' THEN 0 WHEN 'deploying' THEN 1 WHEN 'pending' THEN 1 WHEN 'failed' THEN 2 ELSE 3 END
        OR (
          CASE preferred.status WHEN 'running' THEN 0 WHEN 'deploying' THEN 1 WHEN 'pending' THEN 1 WHEN 'failed' THEN 2 ELSE 3 END = CASE candidate.status WHEN 'running' THEN 0 WHEN 'deploying' THEN 1 WHEN 'pending' THEN 1 WHEN 'failed' THEN 2 ELSE 3 END
          AND (preferred.created_at < candidate.created_at OR (preferred.created_at = candidate.created_at AND preferred.id < candidate.id))
        )
      )
  );

CREATE UNIQUE INDEX applications_one_three_x_ui_master_idx
ON applications(site_id)
WHERE app_key = 'vastora-official/3x-ui' AND role = 'master' AND status IN ('pending', 'deploying', 'running');

CREATE TABLE three_x_ui_nodes (
    worker_application_id TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
    master_application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    remote_node_id INTEGER,
    status TEXT NOT NULL CHECK(status IN ('pending', 'applying', 'ready', 'failed', 'stopped')),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK(worker_application_id <> master_application_id),
    UNIQUE(master_application_id, remote_node_id)
);

CREATE TABLE application_commands_v11 (
    id TEXT PRIMARY KEY,
    application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    gateway_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    kind TEXT NOT NULL CHECK(kind IN ('3xui.reality.create', '3xui.subscription.configure', '3xui.clients.manage', '3xui.node.reconcile')),
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
INSERT INTO application_commands_v11 SELECT * FROM application_commands;
DROP TABLE application_commands;
ALTER TABLE application_commands_v11 RENAME TO application_commands;
CREATE UNIQUE INDEX application_commands_one_active_idx ON application_commands(application_id) WHERE state IN ('pending', 'running');

PRAGMA user_version = 11;
