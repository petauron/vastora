-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;

CREATE TABLE change_proposals_next (
  id TEXT PRIMARY KEY,
  conversation_id TEXT NOT NULL REFERENCES assistant_conversations(id) ON DELETE CASCADE,
  run_id TEXT NOT NULL REFERENCES assistant_runs(id) ON DELETE CASCADE,
  admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK(kind IN ('install_application', 'rotate_cpa_credential')),
  request_json BLOB NOT NULL,
  summary_json BLOB NOT NULL,
  digest TEXT NOT NULL,
  targets_json BLOB NOT NULL,
  expected_revision TEXT NOT NULL,
  policy_version TEXT NOT NULL,
  risk TEXT NOT NULL CHECK(risk IN ('low', 'medium', 'high')),
  status TEXT NOT NULL CHECK(status IN ('pending', 'approved', 'rejected', 'expired', 'applied', 'cancelled')),
  expires_at TEXT NOT NULL,
  deployment_id TEXT REFERENCES deployments(id),
  execution_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

INSERT INTO change_proposals_next(
  id, conversation_id, run_id, admin_id, kind, request_json, summary_json,
  digest, targets_json, expected_revision, policy_version, risk, status,
  expires_at, deployment_id, execution_id, created_at, updated_at
)
SELECT id, conversation_id, run_id, admin_id, kind, request_json, summary_json,
  digest, targets_json, expected_revision, policy_version, risk, status,
  expires_at, deployment_id, COALESCE(deployment_id, ''), created_at, updated_at
FROM change_proposals;

DROP TABLE change_proposals;
ALTER TABLE change_proposals_next RENAME TO change_proposals;
CREATE INDEX change_proposals_conversation_idx ON change_proposals(conversation_id, created_at DESC);

PRAGMA user_version = 54;
PRAGMA foreign_keys = ON;

-- +goose Down
SELECT RAISE(ABORT, 'forward-only migration');
