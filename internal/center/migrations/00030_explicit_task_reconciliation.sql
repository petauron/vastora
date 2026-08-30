-- +goose Up
ALTER TABLE deployments ADD COLUMN reconciliation_requested INTEGER NOT NULL DEFAULT 0 CHECK(reconciliation_requested IN (0, 1));
PRAGMA user_version = 30;

-- +goose Down
SELECT RAISE(ABORT, 'forward-only migration');
