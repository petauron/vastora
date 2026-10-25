-- +goose Up
CREATE TABLE agent_reinstall_current_applications (
 deployment_id TEXT PRIMARY KEY REFERENCES deployments(id),
 operation_id TEXT NOT NULL REFERENCES agent_reinstall_operations(id),
 application_id TEXT NOT NULL REFERENCES applications(id),
 source_deployment_id TEXT NOT NULL REFERENCES deployments(id),
 source_revision TEXT NOT NULL,
 replacement_key BLOB NOT NULL,
 approval_json BLOB NOT NULL,
 operator_verification_json BLOB NOT NULL DEFAULT '{}' CHECK(json_valid(operator_verification_json)),
 sealed_task BLOB NOT NULL,
 UNIQUE(operation_id,application_id)
);
PRAGMA user_version = 122;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
