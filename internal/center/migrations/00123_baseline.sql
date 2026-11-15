-- +goose Up
-- Fresh databases use initializeCurrentSchema. Existing schema 122 databases
-- compact their journal transactionally before the provider is initialized.
PRAGMA user_version=123;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
