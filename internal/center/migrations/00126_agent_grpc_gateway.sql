-- +goose Up
INSERT INTO settings(key,value) VALUES('migration_126_agent_grpc_route','pending');
PRAGMA user_version=126;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
