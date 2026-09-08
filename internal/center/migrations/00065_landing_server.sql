-- +goose Up
CREATE TABLE landing_server_states (
    node_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    desired_revision INTEGER NOT NULL CHECK(desired_revision > 0),
    applied_revision INTEGER NOT NULL DEFAULT 0,
    desired_json BLOB NOT NULL CHECK(json_valid(desired_json)),
	peer_json BLOB NOT NULL DEFAULT '{}',
    status TEXT NOT NULL CHECK(status IN ('pending','applying','ready','failed','stopped')),
    attempt INTEGER NOT NULL DEFAULT 0,
    lease_expires_at TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);
CREATE TABLE landing_proxy_states (
 node_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
 landing_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
 server_revision INTEGER NOT NULL,
 source_address TEXT NOT NULL,
 health_revision INTEGER NOT NULL DEFAULT 0,
 health_ok INTEGER NOT NULL DEFAULT 0 CHECK(health_ok IN (0,1)),
 health_checked_at TEXT NOT NULL DEFAULT '',
 health_received_at TEXT NOT NULL DEFAULT '',
 desired_revision INTEGER NOT NULL CHECK(desired_revision > 0),
 applied_revision INTEGER NOT NULL DEFAULT 0,
 desired_json BLOB NOT NULL CHECK(json_valid(desired_json)),
 status TEXT NOT NULL CHECK(status IN ('pending','applying','ready','failed','stopped')),
 attempt INTEGER NOT NULL DEFAULT 0,
 lease_expires_at TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
);
PRAGMA user_version = 65;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
