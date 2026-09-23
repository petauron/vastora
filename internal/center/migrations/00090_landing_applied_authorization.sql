-- +goose Up
-- Preserve the last confirmed service authorization independently of queued
-- intent. An unapplied or failed desired plan must never become authority.
ALTER TABLE landing_server_states
 ADD COLUMN applied_json BLOB NOT NULL DEFAULT '{}' CHECK(json_valid(applied_json));

UPDATE landing_server_states SET applied_json=desired_json
WHERE applied_revision>0 AND desired_revision=applied_revision
 AND json_extract(desired_json,'$.nodeId')=node_id
 AND json_extract(desired_json,'$.revision')=applied_revision
 AND (
  (status='ready' AND json_type(desired_json,'$.plan')='object'
   AND json_extract(desired_json,'$.plan.revision')=applied_revision)
  OR (status='stopped' AND (json_type(desired_json,'$.plan') IS NULL OR json_type(desired_json,'$.plan')='null'))
 );

PRAGMA user_version = 90;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
