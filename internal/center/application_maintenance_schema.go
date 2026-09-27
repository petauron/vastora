package center

const applicationMaintenanceSchemaSQL = `
CREATE TABLE application_maintenance (
    id TEXT PRIMARY KEY,
    application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    deployment_id TEXT NOT NULL REFERENCES deployments(id),
    action TEXT NOT NULL CHECK(action IN ('logs','backup','restore')),
    backup_id TEXT NOT NULL DEFAULT '',
    resources_json BLOB NOT NULL,
    result_json BLOB NOT NULL DEFAULT '{}',
    state TEXT NOT NULL CHECK(state IN ('pending','running','succeeded','failed')),
    reconciliation_required INTEGER NOT NULL DEFAULT 0 CHECK(reconciliation_required IN (0,1)),
    attempt INTEGER NOT NULL DEFAULT 0,
    lease_expires_at TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX application_maintenance_active ON application_maintenance(application_id)
WHERE state IN ('pending','running') OR reconciliation_required=1;
CREATE TRIGGER deployments_block_during_package_maintenance BEFORE INSERT ON deployments
WHEN EXISTS(SELECT 1 FROM application_maintenance WHERE agent_id=NEW.agent_id AND (state IN ('pending','running') OR reconciliation_required=1))
BEGIN SELECT RAISE(ABORT,'Application package maintenance requires completion or reconciliation'); END;
CREATE TRIGGER deployment_updates_block_during_package_maintenance BEFORE UPDATE ON deployments
WHEN NEW.state IN ('pending','running') AND EXISTS(SELECT 1 FROM application_maintenance WHERE agent_id=NEW.agent_id AND (state IN ('pending','running') OR reconciliation_required=1))
BEGIN SELECT RAISE(ABORT,'Application package maintenance requires completion or reconciliation'); END;
CREATE TRIGGER commands_block_during_package_maintenance BEFORE INSERT ON application_commands
WHEN EXISTS(SELECT 1 FROM application_maintenance WHERE agent_id=NEW.agent_id AND (state IN ('pending','running') OR reconciliation_required=1))
BEGIN SELECT RAISE(ABORT,'Application package maintenance requires completion or reconciliation'); END;
CREATE TRIGGER command_updates_block_during_package_maintenance BEFORE UPDATE ON application_commands
WHEN NEW.state IN ('pending','running') AND EXISTS(SELECT 1 FROM application_maintenance WHERE agent_id=NEW.agent_id AND (state IN ('pending','running') OR reconciliation_required=1))
BEGIN SELECT RAISE(ABORT,'Application package maintenance requires completion or reconciliation'); END;
`
