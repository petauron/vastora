-- +goose Up
CREATE TABLE agent_execution_sessions (
 agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 session_id TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE TABLE agent_execution_session_history (
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 session_id TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(agent_id,session_id)
);
CREATE TABLE task_executions (
 id TEXT PRIMARY KEY,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 task_id TEXT NOT NULL,
 kind TEXT NOT NULL,
 attempt INTEGER NOT NULL,
 session_id TEXT NOT NULL,
 digest TEXT NOT NULL,
 sealed_task BLOB NOT NULL,
 sealed_result BLOB NOT NULL DEFAULT X'',
 state TEXT NOT NULL CHECK(state IN ('offered','running','helper_running','succeeded','failed','unknown')),
 phase TEXT NOT NULL,
 last_error TEXT NOT NULL DEFAULT '',
 expires_at TEXT NOT NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 disposition TEXT NOT NULL DEFAULT '',
 disposition_note TEXT NOT NULL DEFAULT '',
 disposition_actor TEXT NOT NULL DEFAULT '',
 disposed_at TEXT NOT NULL DEFAULT '',
 UNIQUE(task_id, attempt)
);
CREATE INDEX task_executions_agent_unresolved ON task_executions(agent_id, state)
 WHERE disposition='' AND state<>'succeeded';
CREATE TABLE execution_claim_control_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 paused INTEGER NOT NULL CHECK(paused IN (0,1)),
 actor TEXT NOT NULL,
 created_at TEXT NOT NULL
);
CREATE TABLE execution_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 execution_id TEXT NOT NULL REFERENCES task_executions(id) ON DELETE CASCADE,
 phase TEXT NOT NULL,
 state TEXT NOT NULL,
 actor TEXT NOT NULL,
 created_at TEXT NOT NULL
);
-- +goose StatementBegin
CREATE TRIGGER execution_insert_audit AFTER INSERT ON task_executions BEGIN
 INSERT INTO execution_events(execution_id,phase,state,actor,created_at)
 VALUES(NEW.id,NEW.phase,NEW.state,NEW.agent_id,NEW.updated_at);
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER execution_update_audit AFTER UPDATE ON task_executions
 WHEN OLD.state<>NEW.state OR OLD.phase<>NEW.phase OR OLD.disposition<>NEW.disposition BEGIN
 INSERT INTO execution_events(execution_id,phase,state,actor,created_at)
 VALUES(NEW.id,NEW.phase,NEW.state,CASE WHEN NEW.disposition_actor<>'' THEN NEW.disposition_actor ELSE NEW.agent_id END,NEW.updated_at);
END;
-- +goose StatementEnd
-- Existing installations must explicitly coordinate the execution protocol cutover.
-- Fresh databases use executionSchemaSQL and do not require this migration pause.
INSERT INTO settings(key,value)
VALUES('execution_claim_control',json_object('paused',json('true'),'actor','migration:75','updatedAt',strftime('%Y-%m-%dT%H:%M:%fZ','now')))
ON CONFLICT(key) DO UPDATE SET value=excluded.value;
INSERT INTO execution_claim_control_events(paused,actor,created_at)
SELECT 1,'migration:75',json_extract(value,'$.updatedAt') FROM settings WHERE key='execution_claim_control';
PRAGMA user_version = 75;
-- +goose Down
SELECT RAISE(ABORT, 'forward-only migration');
