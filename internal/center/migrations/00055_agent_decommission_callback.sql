-- +goose Up
ALTER TABLE agent_decommissions ADD COLUMN callback_token_hash BLOB NOT NULL DEFAULT X'';

INSERT INTO settings(key, value) VALUES('migration_55_agent_decommission_callback_route', 'pending')
ON CONFLICT(key) DO UPDATE SET value = excluded.value;

PRAGMA user_version = 55;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
