-- +goose Up
ALTER TABLE application_commands ADD COLUMN reconciliation_requested INTEGER NOT NULL DEFAULT 0 CHECK(reconciliation_requested IN (0, 1));
PRAGMA user_version = 49;

-- +goose Down
SELECT RAISE(ABORT, 'forward-only migration');
