-- +goose Up
CREATE TABLE xray_configuration_recoveries (
 agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
 id TEXT NOT NULL UNIQUE,
 action TEXT NOT NULL CHECK(action IN ('inspect','runtime','agent_state')),
 state TEXT NOT NULL CHECK(state IN ('pending','running','awaiting_decision','succeeded','failed')),
 attempt INTEGER NOT NULL DEFAULT 0,
 lease_expires_at TEXT NOT NULL DEFAULT '',
 expected_runtime_sha256 TEXT NOT NULL DEFAULT '',
 expected_agent_sha256 TEXT NOT NULL DEFAULT '',
 expected_agent_revision INTEGER NOT NULL DEFAULT 0,
 error TEXT NOT NULL DEFAULT '',
 result_json TEXT NOT NULL DEFAULT 'null' CHECK(json_valid(result_json)),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);

PRAGMA user_version = 86;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
