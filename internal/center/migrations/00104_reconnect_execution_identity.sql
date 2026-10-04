-- +goose Up
-- Retain previous machine evidence without projecting it onto a replacement.
ALTER TABLE task_executions ADD COLUMN identity_retired_at TEXT NOT NULL DEFAULT '';

DROP TRIGGER execution_update_audit;
-- +goose StatementBegin
CREATE TRIGGER execution_update_audit AFTER UPDATE ON task_executions
 WHEN OLD.state<>NEW.state OR OLD.phase<>NEW.phase OR OLD.disposition<>NEW.disposition OR OLD.identity_retired_at<>NEW.identity_retired_at BEGIN
 INSERT INTO execution_events(execution_id,phase,state,actor,created_at)
 VALUES(NEW.id,NEW.phase,NEW.state,CASE WHEN NEW.disposition_actor<>'' THEN NEW.disposition_actor ELSE NEW.agent_id END,NEW.updated_at);
END;
-- +goose StatementEnd

PRAGMA user_version = 104;
