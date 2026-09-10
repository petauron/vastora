-- +goose Up
-- Extend the command kind constraint without losing queued operations, secrets,
-- row ordering, or exclusion guards. The standard migration runner backs up first.
DROP TRIGGER secret_deliveries_delete_with_application_command;
DROP TRIGGER application_commands_block_during_three_x_ui_migration;
DROP TRIGGER application_command_updates_block_during_three_x_ui_migration;
DROP TRIGGER deployments_block_during_three_x_ui_data_plane;
DROP TRIGGER application_commands_block_during_three_x_ui_deployment;
DROP TRIGGER application_command_updates_block_during_three_x_ui_deployment;

CREATE TABLE application_commands_v71 (
			id TEXT PRIMARY KEY,
			application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
			site_id TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '' COLLATE NOCASE,
			agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			gateway_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
			kind TEXT NOT NULL CHECK(kind IN ('pulse.enrollment.create', '3xui.reality.create', '3xui.reality.verify', '3xui.reality.harden', '3xui.reality.rename', '3xui.reality.remove', '3xui.protocols.configure', '3xui.subscription.configure', '3xui.clients.manage', '3xui.node.reconcile', '3xui.controller.manage')),
			input_json BLOB NOT NULL,
			result_json BLOB NOT NULL DEFAULT '{}',
			result_secret_id TEXT REFERENCES secrets(id) ON DELETE SET NULL,
			state TEXT NOT NULL CHECK(state IN ('pending', 'running', 'succeeded', 'failed')),
			reconciliation_required INTEGER NOT NULL DEFAULT 0 CHECK(reconciliation_required IN (0, 1)),
			reconciliation_requested INTEGER NOT NULL DEFAULT 0 CHECK(reconciliation_requested IN (0, 1)),
			attempt INTEGER NOT NULL DEFAULT 0,
			lease_expires_at TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
INSERT INTO application_commands_v71(rowid,id,application_id,site_id,display_name,agent_id,gateway_node_id,kind,input_json,result_json,result_secret_id,state,reconciliation_required,reconciliation_requested,attempt,lease_expires_at,error,created_at,updated_at)
SELECT rowid,id,application_id,site_id,display_name,agent_id,gateway_node_id,kind,input_json,result_json,result_secret_id,state,reconciliation_required,reconciliation_requested,attempt,lease_expires_at,error,created_at,updated_at FROM application_commands;
DROP TABLE application_commands;
ALTER TABLE application_commands_v71 RENAME TO application_commands;

-- +goose StatementBegin
CREATE TRIGGER secret_deliveries_delete_with_application_command AFTER DELETE ON application_commands
			BEGIN DELETE FROM secret_deliveries WHERE kind = 'application_command_result' AND resource_id = OLD.id; END;
-- +goose StatementEnd

CREATE UNIQUE INDEX application_commands_one_active_idx ON application_commands(agent_id) WHERE (state IN ('pending', 'running') OR reconciliation_required = 1) AND kind NOT IN ('3xui.controller.manage', 'pulse.enrollment.create');

CREATE UNIQUE INDEX application_commands_one_active_controller_idx ON application_commands(application_id) WHERE (state IN ('pending', 'running') OR reconciliation_required = 1) AND kind = '3xui.controller.manage';

CREATE UNIQUE INDEX application_commands_one_active_reality_name_idx ON application_commands(site_id, display_name COLLATE NOCASE)
			WHERE site_id <> '' AND display_name <> '' AND kind IN ('3xui.reality.create', '3xui.reality.rename')
			AND (state IN ('pending', 'running') OR reconciliation_required = 1);

-- +goose StatementBegin
CREATE TRIGGER application_commands_block_during_three_x_ui_migration
			BEFORE INSERT ON application_commands
			WHEN NEW.kind NOT IN ('3xui.controller.manage', 'pulse.enrollment.create')
			AND NOT (NEW.kind = '3xui.node.reconcile' AND EXISTS (
				SELECT 1 FROM three_x_ui_migrations
				WHERE id = json_extract(CASE WHEN json_valid(NEW.input_json) THEN NEW.input_json ELSE '{}' END, '$.migrationId') AND state = 'switching'
			))
			AND EXISTS (SELECT 1 FROM three_x_ui_migrations WHERE state IN ('backing_up', 'restoring', 'switching'))
			BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER application_command_updates_block_during_three_x_ui_migration
			BEFORE UPDATE OF application_id, kind, input_json, state, reconciliation_required ON application_commands
			WHEN NEW.kind NOT IN ('3xui.controller.manage', 'pulse.enrollment.create') AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1)
			AND NOT (NEW.kind = '3xui.node.reconcile' AND EXISTS (
				SELECT 1 FROM three_x_ui_migrations
				WHERE id = json_extract(CASE WHEN json_valid(NEW.input_json) THEN NEW.input_json ELSE '{}' END, '$.migrationId') AND state = 'switching'
			))
			AND EXISTS (SELECT 1 FROM three_x_ui_migrations WHERE state IN ('backing_up', 'restoring', 'switching'))
			BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END;
-- +goose StatementEnd

-- +goose StatementBegin
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
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER application_commands_block_during_three_x_ui_deployment
			BEFORE INSERT ON application_commands
			WHEN NEW.kind NOT IN ('3xui.controller.manage', '3xui.node.reconcile', 'pulse.enrollment.create')
			AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1)
			AND EXISTS (
				SELECT 1 FROM deployments deployment
				WHERE deployment.app_key = 'vastora-official/3x-ui'
				AND (deployment.state IN ('pending', 'running') OR deployment.reconciliation_required = 1)
			)
			BEGIN SELECT RAISE(ABORT, '3x-ui deployment is in progress'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER application_command_updates_block_during_three_x_ui_deployment
			BEFORE UPDATE OF application_id, kind, input_json, state, reconciliation_required ON application_commands
			WHEN NEW.kind NOT IN ('3xui.controller.manage', '3xui.node.reconcile', 'pulse.enrollment.create')
			AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1) AND EXISTS (
				SELECT 1 FROM deployments deployment
				WHERE deployment.app_key = 'vastora-official/3x-ui'
				AND (deployment.state IN ('pending', 'running') OR deployment.reconciliation_required = 1)
			)
			BEGIN SELECT RAISE(ABORT, '3x-ui deployment is in progress'); END;
-- +goose StatementEnd

CREATE UNIQUE INDEX pulse_enrollment_deployment_idx ON application_commands(json_extract(input_json, '$.deploymentId')) WHERE kind = 'pulse.enrollment.create' AND state IN ('pending','running');

PRAGMA user_version = 71;

-- +goose Down
SELECT RAISE(ABORT, 'forward-only migration');

