-- +goose Up
CREATE TABLE IF NOT EXISTS secret_deliveries (
    kind TEXT NOT NULL CHECK(kind IN ('deployment_credentials', 'application_command_result')),
    owner_id TEXT NOT NULL,
    operation_key_hash BLOB NOT NULL,
    request_hash BLOB NOT NULL,
    resource_id TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('pending', 'acknowledged')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(kind, owner_id, operation_key_hash),
    UNIQUE(kind, resource_id)
);
CREATE TRIGGER IF NOT EXISTS secret_deliveries_delete_with_deployment AFTER DELETE ON deployments
BEGIN DELETE FROM secret_deliveries WHERE kind = 'deployment_credentials' AND resource_id = OLD.id; END;
CREATE TRIGGER IF NOT EXISTS secret_deliveries_delete_with_application_command AFTER DELETE ON application_commands
BEGIN DELETE FROM secret_deliveries WHERE kind = 'application_command_result' AND resource_id = OLD.id; END;
PRAGMA user_version = 37;
