-- +goose Up
CREATE TABLE node_security_tasks (
 agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 id TEXT NOT NULL UNIQUE,
 input_json TEXT NOT NULL CHECK(json_valid(input_json)),
 state TEXT NOT NULL CHECK(state IN ('pending','running','succeeded','failed')),
 attempt INTEGER NOT NULL DEFAULT 0,
 lease_expires_at TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 result_json TEXT NOT NULL DEFAULT 'null' CHECK(json_valid(result_json)),
 actor_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
PRAGMA user_version = 114;

-- +goose Down
SELECT 1;
