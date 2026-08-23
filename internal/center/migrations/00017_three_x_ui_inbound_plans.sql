-- +goose Up
CREATE TABLE three_x_ui_inbound_plans (
    service_id TEXT PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    inbound_tag TEXT NOT NULL,
    total_bytes INTEGER NOT NULL DEFAULT 0 CHECK(total_bytes >= 0),
    reset_days INTEGER NOT NULL DEFAULT 0 CHECK(reset_days >= 0),
    next_reset_at TEXT NOT NULL DEFAULT '',
    last_reset_at TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 1 CHECK(revision > 0),
    status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active', 'resetting', 'failed')),
    retry_at TEXT NOT NULL DEFAULT '',
    attempt INTEGER NOT NULL DEFAULT 0 CHECK(attempt >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);

CREATE INDEX three_x_ui_inbound_plans_due_idx
ON three_x_ui_inbound_plans(status, next_reset_at, retry_at)
WHERE reset_days > 0;

-- Existing REALITY inbounds predate deterministic tags, so they remain
-- visible with an inactive plan and require one explicit plan save before
-- scheduled resets can be enabled safely.
INSERT INTO three_x_ui_inbound_plans(
    service_id, inbound_tag, total_bytes, reset_days, next_reset_at,
    last_reset_at, revision, status, retry_at, attempt, last_error, updated_at
)
SELECT id, '', 0, 0, '', '', 1, 'active', '', 0, '', updated_at
FROM services
WHERE app_protocol = 'vless/tcp/reality';

-- In-flight data-plane commands may carry the v16 task shape or an inbound
-- snapshot captured before another application on the same controller Agent.
-- Fail them closed so the operator can retry with v17 serialization and plan
-- semantics instead of leaving a stale command at the front of the queue.
UPDATE application_commands
SET state = 'failed',
    lease_expires_at = '',
    error = 'Center was upgraded; retry this 3x-ui operation with the current traffic plan settings',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE kind <> '3xui.controller.manage'
  AND state IN ('pending', 'running');

-- All 3x-ui data-plane commands execute through the Site controller Agent.
-- Serialize by that Agent, not by the target application, because Worker
-- REALITY commands still run on the Master and share its client snapshot.
DROP INDEX application_commands_one_active_idx;
CREATE UNIQUE INDEX application_commands_one_active_idx
ON application_commands(agent_id)
WHERE state IN ('pending', 'running') AND kind <> '3xui.controller.manage';

-- Migration ownership is Site-wide: Worker REALITY/client commands still run
-- through the Site controller even though their application_id is not the
-- source or target controller application.
DROP TRIGGER application_commands_block_during_three_x_ui_migration;
CREATE TRIGGER application_commands_block_during_three_x_ui_migration
BEFORE INSERT ON application_commands
WHEN NEW.kind <> '3xui.controller.manage'
AND NOT (NEW.kind = '3xui.node.reconcile' AND EXISTS (
    SELECT 1 FROM three_x_ui_migrations
    WHERE id = json_extract(CASE WHEN json_valid(NEW.input_json) THEN NEW.input_json ELSE '{}' END, '$.migrationId')
      AND state = 'switching'
))
AND EXISTS (
    SELECT 1 FROM three_x_ui_migrations migration
    JOIN applications queued ON queued.id = NEW.application_id
    WHERE migration.state IN ('backing_up', 'restoring', 'switching')
      AND migration.site_id = queued.site_id
)
BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END;

DROP TRIGGER IF EXISTS application_command_updates_block_during_three_x_ui_migration;
CREATE TRIGGER application_command_updates_block_during_three_x_ui_migration
BEFORE UPDATE OF application_id, kind, input_json, state ON application_commands
WHEN NEW.kind <> '3xui.controller.manage'
AND NEW.state IN ('pending', 'running')
AND NOT (NEW.kind = '3xui.node.reconcile' AND EXISTS (
    SELECT 1 FROM three_x_ui_migrations
    WHERE id = json_extract(CASE WHEN json_valid(NEW.input_json) THEN NEW.input_json ELSE '{}' END, '$.migrationId')
      AND state = 'switching'
))
AND EXISTS (
    SELECT 1 FROM three_x_ui_migrations migration
    JOIN applications queued ON queued.id = NEW.application_id
    WHERE migration.state IN ('backing_up', 'restoring', 'switching')
      AND migration.site_id = queued.site_id
)
BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END;

PRAGMA user_version = 17;
