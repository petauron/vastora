-- +goose Up
CREATE TABLE task_events_v51 (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    revision INTEGER NOT NULL DEFAULT 0,
    event TEXT NOT NULL CHECK(event IN ('queued', 'claimed', 'released', 'lease_expired', 'succeeded', 'failed')),
    message TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);

INSERT INTO task_events_v51(id, task_id, agent_id, kind, revision, event, message, created_at)
SELECT id, task_id, agent_id, kind, revision, event, message, created_at
FROM task_events;

DROP TABLE task_events;
ALTER TABLE task_events_v51 RENAME TO task_events;
CREATE INDEX task_events_task_idx ON task_events(task_id, created_at);
CREATE INDEX task_events_agent_idx ON task_events(agent_id, created_at DESC);

PRAGMA user_version = 51;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
