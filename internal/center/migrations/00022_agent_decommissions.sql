-- +goose Up
CREATE TABLE agent_decommissions (
    agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    delete_data INTEGER NOT NULL CHECK(delete_data IN (0, 1)),
    state TEXT NOT NULL CHECK(state IN ('pending', 'running', 'succeeded', 'failed', 'abandoned')),
    attempt INTEGER NOT NULL DEFAULT 0,
    lease_expires_at TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

PRAGMA user_version = 22;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
