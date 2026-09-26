-- +goose Up
-- A forward migration must not reinterpret in-flight application operations.
CREATE TABLE catalog_v4_quiescence (unfinished INTEGER NOT NULL CHECK(unfinished = 0));
INSERT INTO catalog_v4_quiescence SELECT COUNT(*) FROM deployments WHERE state IN ('pending','running') OR reconciliation_required=1;
INSERT INTO catalog_v4_quiescence SELECT COUNT(*) FROM application_commands WHERE state IN ('pending','running') OR reconciliation_required=1;
DROP TABLE catalog_v4_quiescence;
-- Preserve old identities byte-for-byte; a new recipe is never a rewrite of a
-- schema 3 identity. The trust root and all anti-rollback state remain untouched.
ALTER TABLE catalog_manifest_history RENAME TO catalog_manifest_history_v3;
CREATE TABLE catalog_manifest_history (
    source_id TEXT NOT NULL,
    app_id TEXT NOT NULL,
    version TEXT NOT NULL,
    package_revision INTEGER NOT NULL CHECK(package_revision >= 0),
    manifest_sha256 TEXT NOT NULL,
    first_seen_at TEXT NOT NULL,
    PRIMARY KEY(source_id, app_id, version, package_revision)
);
INSERT INTO catalog_manifest_history
SELECT source_id, app_id, version, 0, manifest_sha256, first_seen_at
FROM catalog_manifest_history_v3;
DROP TABLE catalog_manifest_history_v3;

CREATE TABLE catalog_legacy_evidence (
    source_id TEXT PRIMARY KEY,
    payload BLOB NOT NULL,
    public_key BLOB NOT NULL
);
INSERT INTO catalog_legacy_evidence SELECT cache.source_id, cache.envelope, source.public_key FROM catalog_cache cache JOIN catalog_sources source ON source.id=cache.source_id;
DELETE FROM catalog_cache;
UPDATE catalog_sources SET last_error = 'Refresh a schema 4 catalog after the maintenance migration.', last_checked_at = '';

ALTER TABLE deployments ADD COLUMN package_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE deployments ADD COLUMN manifest_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN authorized_capabilities_json BLOB NOT NULL DEFAULT '[]';

CREATE TABLE application_resources (
    application_id TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
    manifest_sha256 TEXT NOT NULL DEFAULT '',
    package_revision INTEGER NOT NULL DEFAULT 0,
    authorized_capabilities_json BLOB NOT NULL DEFAULT '[]',
    resources_json BLOB NOT NULL DEFAULT '{}',
    adoption_state TEXT NOT NULL CHECK(adoption_state IN ('pending','ready','blocked')),
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);
INSERT INTO application_resources(application_id, adoption_state, updated_at)
SELECT id, 'pending', updated_at FROM applications;

PRAGMA user_version = 100;

CREATE TABLE application_adoptions (
    id TEXT PRIMARY KEY,
    application_id TEXT NOT NULL UNIQUE REFERENCES applications(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    deployment_id TEXT NOT NULL REFERENCES deployments(id),
    state TEXT NOT NULL CHECK(state IN ('pending','running','succeeded','failed')),
    attempt INTEGER NOT NULL DEFAULT 0,
    lease_expires_at TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

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

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
