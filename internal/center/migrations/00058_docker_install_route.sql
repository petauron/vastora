-- +goose Up
INSERT INTO settings(key, value) VALUES('migration_58_docker_install_route', 'pending')
ON CONFLICT(key) DO UPDATE SET value = excluded.value;

PRAGMA user_version = 58;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
