-- +goose Up
-- Removed client-side chains must be revoked with the installed version
-- before upgrading. Never silently turn an existing grant into new authority.
CREATE TEMP TABLE fixed_landing_cutover_guard (
 remaining INTEGER CONSTRAINT revoke_client_selectable_landing_before_upgrade CHECK(remaining=0)
);
INSERT INTO fixed_landing_cutover_guard
SELECT COUNT(*) FROM landing_client_grants
WHERE json_extract(grant_json,'$.mode')<>'fixed' AND status<>'revoked';
DROP TABLE fixed_landing_cutover_guard;
UPDATE three_x_ui_client_accounts SET mode='fixed',revision=revision+1 WHERE mode<>'fixed';
UPDATE landing_client_grants SET grant_json=json_set(grant_json,'$.mode','fixed')
WHERE status='revoked' AND json_extract(grant_json,'$.mode')<>'fixed';
-- +goose StatementBegin
CREATE TRIGGER fixed_landing_account_insert BEFORE INSERT ON three_x_ui_client_accounts
WHEN NEW.mode<>'fixed'
BEGIN SELECT RAISE(ABORT,'only fixed landing combinations are supported'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER fixed_landing_account_update BEFORE UPDATE OF mode ON three_x_ui_client_accounts
WHEN NEW.mode<>'fixed'
BEGIN SELECT RAISE(ABORT,'only fixed landing combinations are supported'); END;
-- +goose StatementEnd
PRAGMA user_version = 76;
-- +goose Down
SELECT RAISE(ABORT, 'forward-only migration');
