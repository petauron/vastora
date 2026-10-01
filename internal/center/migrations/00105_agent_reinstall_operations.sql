-- +goose Up
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

-- Unused legacy replacement grants have no reviewed operation binding.
DELETE FROM secrets WHERE id IN (SELECT bootstrap_secret_id FROM agent_enrollment_tokens WHERE target_agent_id IS NOT NULL AND used_at IS NULL);
DELETE FROM agent_enrollment_tokens WHERE target_agent_id IS NOT NULL AND used_at IS NULL;

PRAGMA user_version = 105;
