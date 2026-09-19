-- +goose Up
-- The controller replacement workflow must be able to deploy the signed
-- Xray-only runtime onto the demoted source application. All other 3x-ui
-- deployments remain excluded while the migration owns the topology.
DROP TRIGGER deployments_block_during_three_x_ui_migration;
CREATE TRIGGER deployments_block_during_three_x_ui_migration
BEFORE INSERT ON deployments
WHEN NEW.app_key = 'vastora-official/3x-ui'
AND NOT EXISTS (
    SELECT 1 FROM three_x_ui_migrations
    WHERE source_application_id=NEW.application_id AND state='switching'
      AND step='convert_worker' AND last_error=''
)
AND EXISTS (
    SELECT 1 FROM three_x_ui_migrations
    WHERE state IN ('backing_up', 'restoring', 'switching')
)
BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END;

DROP TRIGGER deployments_block_during_three_x_ui_data_plane;
CREATE TRIGGER deployments_block_during_three_x_ui_data_plane
BEFORE INSERT ON deployments
WHEN NEW.app_key = 'vastora-official/3x-ui'
AND NOT EXISTS (
    SELECT 1 FROM three_x_ui_migrations
    WHERE source_application_id=NEW.application_id AND state='switching'
      AND step='convert_worker' AND last_error=''
)
AND EXISTS (
    SELECT 1 FROM application_commands command
    JOIN applications command_app ON command_app.id = command.application_id
    WHERE (command.state IN ('pending', 'running') OR command.reconciliation_required = 1)
      AND command.kind <> '3xui.controller.manage'
      AND command_app.app_key = 'vastora-official/3x-ui'
)
BEGIN SELECT RAISE(ABORT, '3x-ui data-plane operation is in progress'); END;

PRAGMA user_version = 82;

-- +goose Down
SELECT 1;
