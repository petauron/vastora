-- +goose Up
CREATE TABLE agent_removals (
 agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 state TEXT NOT NULL CHECK(state IN ('pending','failed')),
 prepared INTEGER NOT NULL DEFAULT 0 CHECK(prepared IN (0,1)),
 headscale_done INTEGER NOT NULL DEFAULT 0 CHECK(headscale_done IN (0,1)),
 headscale_identity_json BLOB NOT NULL DEFAULT '{}',
 tunnel_done INTEGER NOT NULL DEFAULT 0 CHECK(tunnel_done IN (0,1)),
 last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
PRAGMA user_version = 74;
-- +goose Down
SELECT RAISE(ABORT, 'forward-only migration');
