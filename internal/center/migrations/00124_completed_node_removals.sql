-- +goose Up
-- Completed removals retain only historical platform owners needed by frozen
-- Glivela business foreign keys. No proxy data or protection trigger changes.
CREATE TABLE agent_removals_next (
 agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 state TEXT NOT NULL CHECK(state IN ('pending','failed','complete')),
 prepared INTEGER NOT NULL DEFAULT 0 CHECK(prepared IN (0,1)),
 headscale_done INTEGER NOT NULL DEFAULT 0 CHECK(headscale_done IN (0,1)),
 headscale_identity_json BLOB NOT NULL DEFAULT '{}',
 tunnel_done INTEGER NOT NULL DEFAULT 0 CHECK(tunnel_done IN (0,1)),
 last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
INSERT INTO agent_removals_next SELECT * FROM agent_removals;
DROP TABLE agent_removals;
ALTER TABLE agent_removals_next RENAME TO agent_removals;
PRAGMA user_version=124;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
