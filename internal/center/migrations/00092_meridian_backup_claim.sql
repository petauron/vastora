-- +goose Up
-- The cutover queues its one permitted 3x-ui backup while still in inspect.
-- Once state becomes backup, that pre-existing command must be claimable,
-- while all other legacy mutations remain fenced.
DROP TRIGGER application_command_updates_block_during_meridian_cutover;
-- +goose StatementBegin
CREATE TRIGGER application_command_updates_block_during_meridian_cutover
BEFORE UPDATE OF application_id,kind,input_json,state,reconciliation_required ON application_commands
WHEN NEW.kind GLOB '3xui.*'
AND (NEW.state IN ('pending','running') OR NEW.reconciliation_required=1)
AND EXISTS(SELECT 1 FROM meridian_cutover WHERE id=1 AND state IN ('backup','import','publish','project','verify','retire'))
AND NOT (
 OLD.kind=NEW.kind AND OLD.application_id=NEW.application_id AND OLD.input_json=NEW.input_json
 AND OLD.state='pending' AND NEW.state='running'
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
PRAGMA user_version = 92;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
