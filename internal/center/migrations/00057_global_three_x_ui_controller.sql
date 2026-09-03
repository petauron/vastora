-- +goose Up
-- A controller replacement used to be serialized per Site. Refuse to upgrade
-- an already inconsistent database instead of guessing which active workflow
-- owns the shared 3x-ui control plane.
CREATE TABLE three_x_ui_global_migration_guard (
    active_count INTEGER NOT NULL CHECK(active_count <= 1)
);
INSERT INTO three_x_ui_global_migration_guard(active_count)
SELECT COUNT(*) FROM three_x_ui_migrations
WHERE state IN ('backing_up', 'restoring', 'switching');
DROP TABLE three_x_ui_global_migration_guard;

ALTER TABLE three_x_ui_migrations
ADD COLUMN kind TEXT NOT NULL DEFAULT 'replace'
CHECK(kind IN ('replace', 'consolidate'));

ALTER TABLE three_x_ui_migrations
ADD COLUMN failed_worker_application_id TEXT NOT NULL DEFAULT '';

DROP INDEX three_x_ui_migrations_one_active_idx;
CREATE UNIQUE INDEX three_x_ui_migrations_one_active_idx
ON three_x_ui_migrations((1))
WHERE state IN ('backing_up', 'restoring', 'switching');

-- Keep the canonical choice durable while legacy releases may still have one
-- controller per Site. Connected workers and published control-plane services
-- take precedence, followed by stable creation/id ordering.
CREATE TABLE three_x_ui_control_plane (
    id INTEGER PRIMARY KEY CHECK(id = 1),
    controller_application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
    selection_reason TEXT NOT NULL,
    selected_at TEXT NOT NULL
);

INSERT INTO three_x_ui_control_plane(id, controller_application_id, selection_reason, selected_at)
SELECT 1, controller.id, 'migration:connected-workers-and-publications', strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM applications controller
WHERE controller.app_key = 'vastora-official/3x-ui'
  AND controller.role = 'master'
  AND controller.status IN ('pending', 'deploying', 'running')
ORDER BY
  (SELECT COUNT(*) FROM three_x_ui_nodes topology
   JOIN applications worker ON worker.id = topology.worker_application_id
   WHERE topology.master_application_id = controller.id
     AND worker.status IN ('pending', 'deploying', 'running')) DESC,
  (SELECT COUNT(*) FROM publications publication
   JOIN services service ON service.id = publication.service_id
   WHERE service.application_id = controller.id
     AND service.name IN ('panel', 'subscription')
     AND publication.status <> 'stopped') DESC,
  CASE controller.status WHEN 'running' THEN 0 WHEN 'deploying' THEN 1 ELSE 2 END,
  controller.created_at,
  controller.id
LIMIT 1;

-- SQLite partial indexes reject a legacy database that already contains
-- several Site controllers. Triggers enforce the global rule for every new
-- write while preserving those rows long enough for the backed-up convergence
-- workflow to resolve them safely.
DROP INDEX applications_one_three_x_ui_master_idx;
CREATE TRIGGER applications_one_global_three_x_ui_master_insert
BEFORE INSERT ON applications
WHEN NEW.app_key = 'vastora-official/3x-ui'
  AND NEW.role = 'master'
  AND NEW.status IN ('pending', 'deploying', 'running')
  AND EXISTS (
    SELECT 1 FROM applications existing
    WHERE existing.app_key = 'vastora-official/3x-ui'
      AND existing.role = 'master'
      AND existing.status IN ('pending', 'deploying', 'running')
  )
BEGIN SELECT RAISE(ABORT, 'a global 3x-ui subscription controller already exists'); END;

CREATE TRIGGER applications_one_global_three_x_ui_master_update
BEFORE UPDATE OF app_key, role, status ON applications
WHEN NEW.app_key = 'vastora-official/3x-ui'
  AND NEW.role = 'master'
  AND NEW.status IN ('pending', 'deploying', 'running')
  AND NOT (
    OLD.app_key = 'vastora-official/3x-ui'
    AND OLD.role = 'master'
    AND OLD.status IN ('pending', 'deploying', 'running')
  )
  AND EXISTS (
    SELECT 1 FROM applications existing
    WHERE existing.id <> NEW.id
      AND existing.app_key = 'vastora-official/3x-ui'
      AND existing.role = 'master'
      AND existing.status IN ('pending', 'deploying', 'running')
  )
BEGIN SELECT RAISE(ABORT, 'a global 3x-ui subscription controller already exists'); END;

-- A controller migration now owns the single global control plane, so its
-- exclusion guards must no longer be scoped by Site.
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
    SELECT 1 FROM three_x_ui_migrations
    WHERE state IN ('backing_up', 'restoring', 'switching')
)
BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END;

DROP TRIGGER application_command_updates_block_during_three_x_ui_migration;
CREATE TRIGGER application_command_updates_block_during_three_x_ui_migration
BEFORE UPDATE OF application_id, kind, input_json, state, reconciliation_required ON application_commands
WHEN NEW.kind <> '3xui.controller.manage'
AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1)
AND NOT (NEW.kind = '3xui.node.reconcile' AND EXISTS (
    SELECT 1 FROM three_x_ui_migrations
    WHERE id = json_extract(CASE WHEN json_valid(NEW.input_json) THEN NEW.input_json ELSE '{}' END, '$.migrationId')
      AND state = 'switching'
))
AND EXISTS (
    SELECT 1 FROM three_x_ui_migrations
    WHERE state IN ('backing_up', 'restoring', 'switching')
)
BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END;

DROP TRIGGER deployments_block_during_three_x_ui_migration;
CREATE TRIGGER deployments_block_during_three_x_ui_migration
BEFORE INSERT ON deployments
WHEN NEW.app_key = 'vastora-official/3x-ui' AND EXISTS (
    SELECT 1 FROM three_x_ui_migrations
    WHERE state IN ('backing_up', 'restoring', 'switching')
)
BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END;

DROP TRIGGER deployments_block_during_three_x_ui_data_plane;
CREATE TRIGGER deployments_block_during_three_x_ui_data_plane
BEFORE INSERT ON deployments
WHEN NEW.app_key = 'vastora-official/3x-ui' AND EXISTS (
    SELECT 1 FROM application_commands command
    JOIN applications command_app ON command_app.id = command.application_id
    WHERE (command.state IN ('pending', 'running') OR command.reconciliation_required = 1)
      AND command.kind <> '3xui.controller.manage'
      AND command_app.app_key = 'vastora-official/3x-ui'
)
BEGIN SELECT RAISE(ABORT, '3x-ui data-plane operation is in progress'); END;

DROP TRIGGER application_commands_block_during_three_x_ui_deployment;
CREATE TRIGGER application_commands_block_during_three_x_ui_deployment
BEFORE INSERT ON application_commands
WHEN NEW.kind NOT IN ('3xui.controller.manage', '3xui.node.reconcile')
AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1)
AND EXISTS (
    SELECT 1 FROM deployments deployment
    WHERE deployment.app_key = 'vastora-official/3x-ui'
      AND (deployment.state IN ('pending', 'running') OR deployment.reconciliation_required = 1)
)
BEGIN SELECT RAISE(ABORT, '3x-ui deployment is in progress'); END;

DROP TRIGGER application_command_updates_block_during_three_x_ui_deployment;
CREATE TRIGGER application_command_updates_block_during_three_x_ui_deployment
BEFORE UPDATE OF application_id, kind, input_json, state, reconciliation_required ON application_commands
WHEN NEW.kind NOT IN ('3xui.controller.manage', '3xui.node.reconcile')
AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1)
AND EXISTS (
    SELECT 1 FROM deployments deployment
    WHERE deployment.app_key = 'vastora-official/3x-ui'
      AND (deployment.state IN ('pending', 'running') OR deployment.reconciliation_required = 1)
)
BEGIN SELECT RAISE(ABORT, '3x-ui deployment is in progress'); END;

PRAGMA user_version = 57;
