-- +goose Up
CREATE TABLE agent_enrollment_operations (
    token_hash BLOB PRIMARY KEY REFERENCES agent_enrollment_tokens(token_hash) ON DELETE CASCADE,
    operation_id TEXT NOT NULL UNIQUE,
    request_hash BLOB NOT NULL,
    agent_id TEXT NOT NULL UNIQUE REFERENCES agents(id) ON DELETE CASCADE,
    response_secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);

-- +goose StatementBegin
CREATE TRIGGER agent_enrollment_operation_secret_cleanup
AFTER DELETE ON agent_enrollment_operations
BEGIN
    DELETE FROM secrets WHERE id = OLD.response_secret_id;
END;
-- +goose StatementEnd

PRAGMA user_version = 35;
