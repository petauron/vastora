-- +goose Up
DROP TRIGGER application_commands_block_during_three_x_ui_migration;
CREATE TRIGGER application_commands_block_during_three_x_ui_migration
BEFORE INSERT ON application_commands
WHEN NEW.kind <> '3xui.controller.manage'
AND NOT (NEW.kind = '3xui.node.reconcile' AND EXISTS (
    SELECT 1 FROM three_x_ui_migrations
    WHERE id = json_extract(NEW.input_json, '$.migrationId') AND state = 'switching'
))
AND EXISTS (
    SELECT 1 FROM three_x_ui_migrations
    WHERE state IN ('backing_up', 'restoring', 'switching')
    AND (source_application_id = NEW.application_id OR target_application_id = NEW.application_id)
)
BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END;

-- Center creates and fills this table before v15 while the per-publication
-- certificate secrets still exist. CREATE IF NOT EXISTS also lets databases
-- which already reached v15 apply this migration safely.
CREATE TABLE IF NOT EXISTS site_certificate_handoff (
    site_id TEXT PRIMARY KEY,
    dns_names_json BLOB NOT NULL,
    secret_id TEXT NOT NULL UNIQUE,
    sealed BLOB NOT NULL,
    not_after TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT INTO secrets(id, sealed, created_at, updated_at)
SELECT secret_id, sealed, created_at, updated_at
FROM site_certificate_handoff;

INSERT INTO site_certificates(site_id, dns_names_json, secret_id, not_after, status, last_error, created_at, updated_at)
SELECT site_id, dns_names_json, secret_id, not_after, 'ready', 'migration handoff pending', created_at, updated_at
FROM site_certificate_handoff
WHERE 1
ON CONFLICT(site_id) DO UPDATE SET
    dns_names_json = excluded.dns_names_json,
    secret_id = excluded.secret_id,
    not_after = excluded.not_after,
    status = 'ready',
    last_error = 'migration handoff pending',
    updated_at = excluded.updated_at;

UPDATE publications
SET status = 'pending',
    last_error = '',
    desired_revision = desired_revision + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE status <> 'stopped'
  AND tls_enabled = 1
  AND kind IN ('lan_gateway', 'headscale_gateway')
  AND service_id IN (
      SELECT id FROM services
      WHERE site_id IN (SELECT site_id FROM site_certificate_handoff)
  );

DROP TABLE site_certificate_handoff;
PRAGMA user_version = 16;
