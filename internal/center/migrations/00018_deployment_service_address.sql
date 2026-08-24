-- +goose Up
ALTER TABLE deployments
ADD COLUMN service_address TEXT NOT NULL DEFAULT '';

ALTER TABLE deployments
ADD COLUMN reconciliation_required INTEGER NOT NULL DEFAULT 0 CHECK(reconciliation_required IN (0, 1));

ALTER TABLE application_commands
ADD COLUMN reconciliation_required INTEGER NOT NULL DEFAULT 0 CHECK(reconciliation_required IN (0, 1));

-- Preserve the address selected for any deployment that was already queued
-- when Center upgraded. Future tasks capture it atomically at creation time.
UPDATE deployments
SET service_address = COALESCE((
    SELECT profile.service_address
    FROM agent_network_profiles profile
    WHERE profile.agent_id = deployments.agent_id
), '');

DROP INDEX deployments_one_active_task_idx;
CREATE UNIQUE INDEX deployments_one_active_task_idx
ON deployments(agent_id, app_key)
WHERE state IN ('pending', 'running') OR reconciliation_required = 1;

DROP INDEX application_commands_one_active_idx;
CREATE UNIQUE INDEX application_commands_one_active_idx
ON application_commands(agent_id)
WHERE (state IN ('pending', 'running') OR reconciliation_required = 1)
  AND kind <> '3xui.controller.manage';

DROP INDEX application_commands_one_active_controller_idx;
CREATE UNIQUE INDEX application_commands_one_active_controller_idx
ON application_commands(application_id)
WHERE (state IN ('pending', 'running') OR reconciliation_required = 1)
  AND kind = '3xui.controller.manage';

CREATE TRIGGER deployments_block_during_three_x_ui_data_plane
BEFORE INSERT ON deployments
WHEN NEW.app_key = 'vastora-official/3x-ui' AND EXISTS (
    SELECT 1 FROM application_commands command
    JOIN applications command_app ON command_app.id = command.application_id
    JOIN applications deployment_app ON deployment_app.id = NEW.application_id
    WHERE (command.state IN ('pending', 'running') OR command.reconciliation_required = 1)
      AND command.kind <> '3xui.controller.manage'
      AND command_app.site_id = deployment_app.site_id
)
BEGIN SELECT RAISE(ABORT, '3x-ui data-plane operation is in progress'); END;

CREATE TRIGGER application_commands_block_during_three_x_ui_deployment
BEFORE INSERT ON application_commands
WHEN NEW.kind NOT IN ('3xui.controller.manage', '3xui.node.reconcile')
  AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1)
  AND EXISTS (
    SELECT 1 FROM deployments deployment
    JOIN applications deployment_app ON deployment_app.id = deployment.application_id
    JOIN applications command_app ON command_app.id = NEW.application_id
    WHERE deployment.app_key = 'vastora-official/3x-ui'
      AND (deployment.state IN ('pending', 'running') OR deployment.reconciliation_required = 1)
      AND deployment_app.site_id = command_app.site_id
)
BEGIN SELECT RAISE(ABORT, '3x-ui deployment is in progress'); END;

CREATE TRIGGER application_command_updates_block_during_three_x_ui_deployment
BEFORE UPDATE OF application_id, kind, input_json, state, reconciliation_required ON application_commands
WHEN NEW.kind NOT IN ('3xui.controller.manage', '3xui.node.reconcile')
  AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1) AND EXISTS (
    SELECT 1 FROM deployments deployment
    JOIN applications deployment_app ON deployment_app.id = deployment.application_id
    JOIN applications command_app ON command_app.id = NEW.application_id
    WHERE deployment.app_key = 'vastora-official/3x-ui'
      AND (deployment.state IN ('pending', 'running') OR deployment.reconciliation_required = 1)
      AND deployment_app.site_id = command_app.site_id
)
BEGIN SELECT RAISE(ABORT, '3x-ui deployment is in progress'); END;

-- Version 17 allowed a deployment on a Worker and a data-plane command on the
-- Site controller to be active concurrently. Exercise the new UPDATE guard so
-- upgrading such a database fails closed instead of silently preserving a
-- state race across two Agents.
UPDATE application_commands
SET state = state
WHERE kind NOT IN ('3xui.controller.manage', '3xui.node.reconcile')
  AND (state IN ('pending', 'running') OR reconciliation_required = 1);

PRAGMA user_version = 18;
