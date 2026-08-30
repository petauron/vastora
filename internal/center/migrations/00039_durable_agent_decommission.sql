-- +goose Up
CREATE TABLE agent_decommissions_v39 (
    agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    delete_data INTEGER NOT NULL CHECK(delete_data IN (0, 1)),
    state TEXT NOT NULL CHECK(state IN ('pending', 'running', 'cleaning', 'succeeded', 'failed', 'abandoned')),
    attempt INTEGER NOT NULL DEFAULT 0,
    lease_expires_at TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT INTO agent_decommissions_v39(agent_id, delete_data, state, attempt, lease_expires_at, last_error, created_at, updated_at)
SELECT agent_id, delete_data, state, attempt, lease_expires_at, last_error, created_at, updated_at
FROM agent_decommissions;

DROP TABLE agent_decommissions;
ALTER TABLE agent_decommissions_v39 RENAME TO agent_decommissions;

PRAGMA user_version = 39;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
