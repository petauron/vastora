-- +goose Up
CREATE TABLE IF NOT EXISTS application_credential_rotations (
  id TEXT PRIMARY KEY,
  application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
  admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
  operation_key_hash BLOB NOT NULL,
  request_hash BLOB NOT NULL,
  target TEXT NOT NULL CHECK(target IN ('management', 'client')),
  secret_id TEXT REFERENCES secrets(id) ON DELETE RESTRICT,
  cpa_deployment_id TEXT NOT NULL,
  keeper_deployment_id TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL CHECK(state IN ('preparing', 'pending', 'succeeded', 'failed', 'action_required')),
  last_error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(admin_id, operation_key_hash),
  UNIQUE(cpa_deployment_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS application_credential_rotations_keeper_idx
ON application_credential_rotations(keeper_deployment_id)
WHERE keeper_deployment_id <> '';

PRAGMA user_version = 53;

-- +goose Down
SELECT RAISE(ABORT, 'forward-only migration');
