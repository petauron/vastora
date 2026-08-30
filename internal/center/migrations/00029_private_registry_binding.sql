-- +goose Up
ALTER TABLE deployments ADD COLUMN registry_credential_id TEXT REFERENCES registry_credentials(id) ON DELETE RESTRICT;
PRAGMA user_version = 29;

-- +goose Down
SELECT RAISE(ABORT, 'center database migrations are forward-only');
