-- +goose Up
CREATE TABLE agent_reinstall_operations (
 id TEXT PRIMARY KEY,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 authorized_by TEXT NOT NULL,
 plan_revision TEXT NOT NULL,
 plan_json BLOB NOT NULL CHECK(json_valid(plan_json)),
 previous_fingerprint TEXT NOT NULL,
 replacement_fingerprint TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL CHECK(state IN ('preparing','awaiting_enrollment','review_required','failed','superseded','completed')),
 enrollment_token_hash BLOB,
 sealed_enrollment BLOB,
 last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX agent_reinstall_active_idx ON agent_reinstall_operations(agent_id) WHERE state NOT IN ('superseded','completed');

-- Unused legacy replacement grants have no reviewed operation binding.
DELETE FROM secrets WHERE id IN (SELECT bootstrap_secret_id FROM agent_enrollment_tokens WHERE target_agent_id IS NOT NULL AND used_at IS NULL);
DELETE FROM agent_enrollment_tokens WHERE target_agent_id IS NOT NULL AND used_at IS NULL;

PRAGMA user_version = 105;
