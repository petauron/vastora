-- +goose Up
ALTER TABLE landing_proxy_states ADD COLUMN applied_landing_node_id TEXT;

-- Only reconstruct a confirmed current revision. A pending/failed switch does
-- not contain enough information to infer the previously applied exit.
UPDATE landing_proxy_states
SET applied_landing_node_id = CASE
    WHEN json_extract(desired_json, '$.proxy') IS NULL THEN ''
    ELSE landing_node_id END
WHERE applied_revision = desired_revision AND applied_revision > 0
  AND status IN ('ready', 'stopped');

PRAGMA user_version = 68;
