-- +goose Up
CREATE TABLE node_diagnostic_checks (
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 kind TEXT NOT NULL CHECK(kind IN ('node.network-quality','node.return-route','node.international-bandwidth')),
 id TEXT NOT NULL UNIQUE,
 bind_address TEXT NOT NULL,
 target_revision INTEGER NOT NULL,
 targets_json TEXT NOT NULL CHECK(json_valid(targets_json)),
 state TEXT NOT NULL CHECK(state IN ('pending','running','succeeded','failed')),
 attempt INTEGER NOT NULL DEFAULT 0,
 lease_expires_at TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 result_json TEXT NOT NULL DEFAULT 'null' CHECK(json_valid(result_json)),
 checked_at TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(agent_id,kind)
);

-- +goose Down
SELECT 1;
