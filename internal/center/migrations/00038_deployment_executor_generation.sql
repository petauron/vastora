-- +goose Up
ALTER TABLE deployments ADD COLUMN executed_runtime_generation INTEGER CHECK(executed_runtime_generation >= 0);
PRAGMA user_version = 38;

-- +goose Down
SELECT RAISE(ABORT, 'center database migrations are forward-only');
