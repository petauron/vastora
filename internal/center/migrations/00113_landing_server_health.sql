-- +goose Up
ALTER TABLE landing_server_states ADD COLUMN health_json BLOB NOT NULL DEFAULT '{}' CHECK(json_valid(health_json));
ALTER TABLE landing_server_states ADD COLUMN health_received_at TEXT NOT NULL DEFAULT '';
PRAGMA user_version = 113;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
