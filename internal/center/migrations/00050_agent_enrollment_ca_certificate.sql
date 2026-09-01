-- +goose Up
ALTER TABLE agent_enrollment_tokens ADD COLUMN ca_certificate_pem TEXT NOT NULL DEFAULT '';
PRAGMA user_version = 50;

-- +goose Down
SELECT RAISE(ABORT, 'forward-only migration');
