-- +goose Up
ALTER TABLE agent_enrollment_tokens
ADD COLUMN target_agent_id TEXT REFERENCES agents(id) ON DELETE CASCADE;

CREATE UNIQUE INDEX agent_enrollment_one_reconnect_idx
ON agent_enrollment_tokens(target_agent_id)
WHERE target_agent_id IS NOT NULL;

PRAGMA user_version = 59;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
