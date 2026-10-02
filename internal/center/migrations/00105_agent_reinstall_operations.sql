-- +goose NO TRANSACTION
-- +goose Up
-- The runner backs up first. Rebuild the command constraint without deleting
-- existing task, delivery or recovery evidence. Commit only a valid graph.
PRAGMA foreign_keys = OFF;
PRAGMA legacy_alter_table = ON;
BEGIN IMMEDIATE;

DROP TRIGGER secret_deliveries_delete_with_application_command;
DROP TRIGGER application_commands_block_during_three_x_ui_migration;
DROP TRIGGER application_command_updates_block_during_three_x_ui_migration;
DROP TRIGGER application_commands_block_during_three_x_ui_deployment;
DROP TRIGGER application_command_updates_block_during_three_x_ui_deployment;
DROP TRIGGER application_commands_block_during_meridian_cutover;
DROP TRIGGER application_command_updates_block_during_meridian_cutover;

CREATE TABLE application_commands_v105 (
			id TEXT PRIMARY KEY,
			application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
			site_id TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '' COLLATE NOCASE,
			agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			gateway_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
			kind TEXT NOT NULL CHECK(kind IN ('meridian.runtime.apply', 'meridian.legacy.export', 'meridian.legacy.retire', 'meridian.subscription.publish', 'pulse.enrollment.create', 'pulse.enrollment.inspect', 'pulse.node.rotate', '3xui.reality.create', '3xui.reality.verify', '3xui.reality.harden', '3xui.reality.rename', '3xui.reality.remove', '3xui.protocols.configure', '3xui.subscription.configure', '3xui.clients.manage', '3xui.node.reconcile', '3xui.controller.manage')),
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
INSERT INTO application_commands_v105(rowid,id,application_id,site_id,display_name,agent_id,gateway_node_id,kind,input_json,result_json,result_secret_id,state,reconciliation_required,reconciliation_requested,attempt,lease_expires_at,error,created_at,updated_at) SELECT rowid,id,application_id,site_id,display_name,agent_id,gateway_node_id,kind,input_json,result_json,result_secret_id,state,reconciliation_required,reconciliation_requested,attempt,lease_expires_at,error,created_at,updated_at FROM application_commands;
DROP TABLE application_commands;
ALTER TABLE application_commands_v105 RENAME TO application_commands;

-- +goose StatementBegin
CREATE TRIGGER secret_deliveries_delete_with_application_command AFTER DELETE ON application_commands
			BEGIN DELETE FROM secret_deliveries WHERE kind = 'application_command_result' AND resource_id = OLD.id; END;
-- +goose StatementEnd

CREATE UNIQUE INDEX application_commands_one_active_idx ON application_commands(agent_id) WHERE (state IN ('pending', 'running') OR reconciliation_required = 1) AND kind NOT IN ('3xui.controller.manage', 'pulse.enrollment.create');

CREATE UNIQUE INDEX pulse_enrollment_deployment_idx ON application_commands(json_extract(input_json, '$.deploymentId')) WHERE kind = 'pulse.enrollment.create' AND state IN ('pending','running');

CREATE UNIQUE INDEX application_commands_one_active_controller_idx ON application_commands(application_id) WHERE (state IN ('pending', 'running') OR reconciliation_required = 1) AND kind = '3xui.controller.manage';

CREATE UNIQUE INDEX application_commands_one_active_reality_name_idx ON application_commands(site_id, display_name COLLATE NOCASE)
			WHERE site_id <> '' AND display_name <> '' AND kind IN ('3xui.reality.create', '3xui.reality.rename')
			AND (state IN ('pending', 'running') OR reconciliation_required = 1);

-- +goose StatementBegin
CREATE TRIGGER application_commands_block_during_three_x_ui_migration
			BEFORE INSERT ON application_commands
			WHEN NEW.kind NOT IN ('3xui.controller.manage', 'pulse.enrollment.create', 'pulse.enrollment.inspect', 'pulse.node.rotate')
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
			WHEN NEW.kind NOT IN ('3xui.controller.manage', 'pulse.enrollment.create', 'pulse.enrollment.inspect', 'pulse.node.rotate') AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1)
			AND NOT (NEW.kind = '3xui.node.reconcile' AND EXISTS (
				SELECT 1 FROM three_x_ui_migrations
				WHERE id = json_extract(CASE WHEN json_valid(NEW.input_json) THEN NEW.input_json ELSE '{}' END, '$.migrationId') AND state = 'switching'
			))
			AND EXISTS (SELECT 1 FROM three_x_ui_migrations WHERE state IN ('backing_up', 'restoring', 'switching'))
			BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER application_commands_block_during_three_x_ui_deployment
			BEFORE INSERT ON application_commands
			WHEN NEW.kind NOT IN ('3xui.controller.manage', '3xui.node.reconcile', 'pulse.enrollment.create', 'pulse.enrollment.inspect', 'pulse.node.rotate')
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
			WHEN NEW.kind NOT IN ('3xui.controller.manage', '3xui.node.reconcile', 'pulse.enrollment.create', 'pulse.enrollment.inspect', 'pulse.node.rotate')
			AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1) AND EXISTS (
				SELECT 1 FROM deployments deployment
				WHERE deployment.app_key = 'vastora-official/3x-ui'
				AND (deployment.state IN ('pending', 'running') OR deployment.reconciliation_required = 1)
			)
			BEGIN SELECT RAISE(ABORT, '3x-ui deployment is in progress'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER application_commands_block_during_meridian_cutover
BEFORE INSERT ON application_commands
WHEN NEW.kind GLOB '3xui.*'
AND EXISTS(SELECT 1 FROM meridian_cutover WHERE id=1 AND state IN ('backup','import','publish','project','verify','retire'))
BEGIN SELECT RAISE(ABORT, 'Meridian authority cutover is in progress'); END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER application_command_updates_block_during_meridian_cutover
BEFORE UPDATE OF application_id,kind,input_json,state,reconciliation_required ON application_commands
WHEN NEW.kind GLOB '3xui.*'
AND (NEW.state IN ('pending','running') OR NEW.reconciliation_required=1)
AND EXISTS(SELECT 1 FROM meridian_cutover WHERE id=1 AND state IN ('backup','import','publish','project','verify','retire'))
AND NOT (
 OLD.kind=NEW.kind AND OLD.application_id=NEW.application_id AND OLD.input_json=NEW.input_json
 AND OLD.state IN ('pending','running') AND NEW.state='running'
 AND OLD.reconciliation_required=0 AND NEW.reconciliation_required=0
 AND NEW.kind='3xui.controller.manage'
 AND EXISTS(SELECT 1 FROM meridian_cutover cutover WHERE cutover.id=1 AND cutover.state='backup'
  AND cutover.legacy_controller_application_id=NEW.application_id
  AND cutover.backup_revision=json_extract(CASE WHEN json_valid(NEW.input_json) THEN NEW.input_json ELSE '{}' END,'$.backupRevision')
  AND json_extract(CASE WHEN json_valid(NEW.input_json) THEN NEW.input_json ELSE '{}' END,'$.action')='backup'
  AND COALESCE(json_extract(CASE WHEN json_valid(NEW.input_json) THEN NEW.input_json ELSE '{}' END,'$.migrationId'),'')='')
)
BEGIN SELECT RAISE(ABORT, 'Meridian authority cutover is in progress'); END;
-- +goose StatementEnd

CREATE TABLE agent_reinstall_operations (
 id TEXT PRIMARY KEY,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 authorized_by TEXT NOT NULL,
 plan_revision TEXT NOT NULL,
 plan_json BLOB NOT NULL CHECK(json_valid(plan_json)),
 previous_fingerprint TEXT NOT NULL,
 replacement_fingerprint TEXT NOT NULL DEFAULT '',
 private_identity_json BLOB NOT NULL DEFAULT '{}' CHECK(json_valid(private_identity_json)),
 private_isolation TEXT NOT NULL DEFAULT 'pending' CHECK(private_isolation IN ('pending','not_required','withdrawn')),
 replacement_peer_json BLOB NOT NULL DEFAULT '{}' CHECK(json_valid(replacement_peer_json)),
 replacement_network_observed_at TEXT NOT NULL DEFAULT '',
 attempt INTEGER NOT NULL DEFAULT 1 CHECK(attempt>0),
 state TEXT NOT NULL CHECK(state IN ('preparing','awaiting_enrollment','review_required','failed','superseded','completed')),
 enrollment_token_hash BLOB,
 sealed_enrollment BLOB,
 last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX agent_reinstall_active_idx ON agent_reinstall_operations(agent_id) WHERE state NOT IN ('superseded','completed');
CREATE TABLE agent_reinstall_network_approvals (
 operation_id TEXT NOT NULL REFERENCES agent_reinstall_operations(id) ON DELETE CASCADE,
 plan_revision TEXT NOT NULL,
 approval_json BLOB NOT NULL CHECK(json_valid(approval_json)),
 PRIMARY KEY(operation_id,plan_revision)
);
CREATE TABLE agent_reinstall_local_dispositions (
 operation_id TEXT NOT NULL REFERENCES agent_reinstall_operations(id) ON DELETE CASCADE,
 plan_revision TEXT NOT NULL,
 disposition_json BLOB NOT NULL CHECK(json_valid(disposition_json)),
 PRIMARY KEY(operation_id,plan_revision)
);
CREATE TABLE agent_reinstall_app_preparations (
 deployment_id TEXT PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
 source_deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
 operation_id TEXT NOT NULL REFERENCES agent_reinstall_operations(id) ON DELETE CASCADE,
 application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
 plan_revision TEXT NOT NULL,
 source_revision TEXT NOT NULL,
 replacement_key BLOB NOT NULL,
 approval_json BLOB NOT NULL CHECK(json_valid(approval_json)),
 task_json BLOB NOT NULL CHECK(json_valid(task_json)),
 runtime_command_id TEXT UNIQUE REFERENCES application_commands(id) ON DELETE CASCADE,
 runtime_endpoint_id TEXT REFERENCES meridian_endpoints(id) ON DELETE CASCADE,
 runtime_plan_revision TEXT NOT NULL DEFAULT '',
 runtime_revision INTEGER NOT NULL DEFAULT 0,
 runtime_task_sha256 TEXT NOT NULL DEFAULT '',
 listener_task_id TEXT UNIQUE,
 listener_revision INTEGER NOT NULL DEFAULT 0,
 listener_attempt INTEGER NOT NULL DEFAULT 0,
 listener_plan_revision TEXT NOT NULL DEFAULT '',
 listener_task_sha256 TEXT NOT NULL DEFAULT '',
 listener_state TEXT NOT NULL DEFAULT '' CHECK(listener_state IN ('','pending','running','succeeded','failed')),
 UNIQUE(operation_id,application_id)
);
CREATE TABLE agent_reinstall_dns_migrations (
 id TEXT PRIMARY KEY,
 preparation_id TEXT NOT NULL REFERENCES agent_reinstall_app_preparations(deployment_id) ON DELETE CASCADE,
 attempt INTEGER NOT NULL CHECK(attempt>0),
 plan_revision TEXT NOT NULL,
 targets_json BLOB NOT NULL CHECK(json_valid(targets_json)),
 result_json BLOB NOT NULL CHECK(json_valid(result_json)),
 UNIQUE(preparation_id,attempt)
);
CREATE TABLE agent_reinstall_access_activations (
 preparation_id TEXT PRIMARY KEY REFERENCES agent_reinstall_app_preparations(deployment_id) ON DELETE CASCADE,
 plan_revision TEXT NOT NULL,
 profile_json BLOB NOT NULL CHECK(json_valid(profile_json)),
 service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
 service_endpoint TEXT NOT NULL,
 activated_at TEXT NOT NULL
);
CREATE TABLE agent_reinstall_entry_checks (
 id TEXT PRIMARY KEY,
 preparation_id TEXT NOT NULL REFERENCES agent_reinstall_app_preparations(deployment_id) ON DELETE CASCADE,
 plan_revision TEXT NOT NULL,
 started_at TEXT NOT NULL,
 result_json BLOB NOT NULL CHECK(json_valid(result_json))
);
CREATE TABLE agent_reinstall_monitor_inspections (
 command_id TEXT PRIMARY KEY REFERENCES application_commands(id) ON DELETE CASCADE,
 operation_id TEXT NOT NULL REFERENCES agent_reinstall_operations(id) ON DELETE CASCADE,
 application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
 plan_revision TEXT NOT NULL,
 replacement_key BLOB NOT NULL,
 input_json BLOB NOT NULL CHECK(json_valid(input_json)),
 result_json BLOB NOT NULL DEFAULT '{}' CHECK(json_valid(result_json)),
 UNIQUE(operation_id,application_id,plan_revision)
);


CREATE TABLE agent_reinstall_landing_sources (
 preparation_id TEXT PRIMARY KEY REFERENCES agent_reinstall_app_preparations(deployment_id) ON DELETE CASCADE,
 endpoint_id TEXT NOT NULL REFERENCES meridian_endpoints(id) ON DELETE CASCADE,
 phase TEXT NOT NULL CHECK(phase IN ('withdraw','authorize')),
 identity_json BLOB NOT NULL CHECK(json_valid(identity_json)),
 targets_json BLOB NOT NULL CHECK(json_valid(targets_json)),
 plan_revision TEXT NOT NULL,
 authorized_revision TEXT NOT NULL DEFAULT ''
);

CREATE TABLE agent_reinstall_monitor_rotations (
 command_id TEXT PRIMARY KEY REFERENCES application_commands(id) ON DELETE CASCADE,
 operation_id TEXT NOT NULL REFERENCES agent_reinstall_operations(id) ON DELETE CASCADE,
 application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
 inspection_id TEXT NOT NULL REFERENCES agent_reinstall_monitor_inspections(command_id) ON DELETE RESTRICT,
 plan_revision TEXT NOT NULL,
 input_json BLOB NOT NULL CHECK(json_valid(input_json)),
 result_json BLOB NOT NULL DEFAULT '{}' CHECK(json_valid(result_json)),
 UNIQUE(operation_id,application_id)
);

CREATE TABLE agent_reinstall_monitor_restorations (
 deployment_id TEXT PRIMARY KEY REFERENCES deployments(id) ON DELETE CASCADE,
 rotation_command_id TEXT NOT NULL UNIQUE REFERENCES agent_reinstall_monitor_rotations(command_id) ON DELETE RESTRICT,
 source_deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE RESTRICT,
 plan_revision TEXT NOT NULL,
 approval_json BLOB NOT NULL CHECK(json_valid(approval_json)),
 task_sha256 TEXT NOT NULL
);

-- Unused legacy replacement grants have no reviewed operation binding.
DELETE FROM secrets WHERE id IN (SELECT bootstrap_secret_id FROM agent_enrollment_tokens WHERE target_agent_id IS NOT NULL AND used_at IS NULL);
DELETE FROM agent_enrollment_tokens WHERE target_agent_id IS NOT NULL AND used_at IS NULL;



CREATE TEMP TABLE migration_105_integrity(valid INTEGER CHECK(valid=1));
INSERT INTO migration_105_integrity SELECT NOT EXISTS(SELECT 1 FROM pragma_foreign_key_check);
DROP TABLE migration_105_integrity;
PRAGMA user_version = 105;
COMMIT;
PRAGMA legacy_alter_table = OFF;
PRAGMA foreign_keys = ON;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
