-- +goose Up
ALTER TABLE agents ADD COLUMN x25519_public_key BLOB NOT NULL DEFAULT X'';
ALTER TABLE agents ADD COLUMN credential_revoked_at TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_enrollment_tokens ADD COLUMN ca_fingerprint TEXT NOT NULL DEFAULT '';

PRAGMA user_version = 28;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
