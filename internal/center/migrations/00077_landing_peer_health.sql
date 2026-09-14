-- +goose Up
ALTER TABLE landing_proxy_states ADD COLUMN peer_health_json BLOB NOT NULL DEFAULT '[]' CHECK(json_valid(peer_health_json));
PRAGMA user_version = 77;
-- +goose Down
SELECT RAISE(ABORT, 'forward-only migration');
