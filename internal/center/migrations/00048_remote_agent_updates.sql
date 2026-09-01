-- +goose Up
ALTER TABLE agents ADD COLUMN remote_update_supported INTEGER NOT NULL DEFAULT 0
    CHECK(remote_update_supported IN (0, 1));

CREATE TABLE agent_updates (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    target_version TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('pending', 'running', 'installing', 'succeeded', 'failed')),
    attempt INTEGER NOT NULL DEFAULT 0,
    lease_expires_at TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX agent_updates_one_active_idx
ON agent_updates(agent_id)
WHERE state IN ('pending', 'running', 'installing');
CREATE INDEX agent_updates_agent_idx ON agent_updates(agent_id, created_at DESC);

PRAGMA user_version = 48;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
