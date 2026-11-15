-- +goose Up
CREATE TABLE tailscale_update_tasks (
 id TEXT PRIMARY KEY,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 target_version TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','running','awaiting_decision','succeeded','failed','abandoned')),
 attempt INTEGER NOT NULL DEFAULT 0,
 lease_expires_at TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 actor_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE INDEX tailscale_update_tasks_agent_idx ON tailscale_update_tasks(agent_id, created_at DESC);
PRAGMA user_version=125;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
