-- +goose Up
-- A topology cutover is unsafe while an old per-entry operation can still
-- report. Operators must resolve those executions with the installed version
-- before the global pool becomes authoritative.
CREATE TEMP TABLE global_landing_pool_cutover_guard (
 remaining INTEGER CONSTRAINT resolve_landing_operations_before_upgrade CHECK(remaining=0)
);
INSERT INTO global_landing_pool_cutover_guard
SELECT
 (SELECT COUNT(*) FROM landing_client_grants
   WHERE status NOT IN ('ready','revoked')) +
 (SELECT COUNT(*) FROM landing_proxy_states
   WHERE status NOT IN ('ready','stopped')) +
 (SELECT COUNT(*) FROM landing_server_states
   WHERE status NOT IN ('ready','stopped')) +
 (SELECT COUNT(*) FROM application_commands
   WHERE kind='3xui.clients.manage'
     AND json_extract(CASE WHEN json_valid(input_json) THEN input_json ELSE '{}' END,'$.action')='landing_grant'
     AND (state IN ('pending','running') OR reconciliation_required=1)) +
 (SELECT COUNT(*) FROM task_executions execution
   JOIN application_commands command ON command.id=execution.task_id
   WHERE execution.disposition='' AND execution.state<>'succeeded'
     AND command.kind='3xui.clients.manage'
     AND json_extract(CASE WHEN json_valid(command.input_json) THEN command.input_json ELSE '{}' END,'$.action')='landing_grant');
DROP TABLE global_landing_pool_cutover_guard;

CREATE TEMP TABLE global_landing_pool_data_guard (
 remaining INTEGER CONSTRAINT repair_invalid_landing_policy_before_upgrade CHECK(remaining=0)
);
INSERT INTO global_landing_pool_data_guard
SELECT COUNT(*) FROM settings
WHERE (key LIKE 'node-exits:%' AND key NOT LIKE 'node-exits-error:%'
       AND (NOT json_valid(value)
            OR json_extract(value,'$.revision') IS NULL
            OR json_type(value,'$.landingNodeIds')<>'array'
            OR COALESCE(json_type(value,'$.landingRegionCodes'),'object')<>'object'))
   OR (key='three_x_ui_landing_selection'
       AND (NOT json_valid(value)
            OR json_extract(value,'$.revision') IS NULL
            OR json_type(value,'$.nodeIds')<>'array'));
INSERT INTO global_landing_pool_data_guard
SELECT COUNT(*) FROM (
 SELECT region.key
 FROM settings policy, json_each(policy.value,'$.landingRegionCodes') region
 WHERE policy.key LIKE 'node-exits:%' AND policy.key NOT LIKE 'node-exits-error:%'
 GROUP BY region.key
 HAVING COUNT(DISTINCT region.value)>1
    OR MAX(TYPEOF(region.value)<>'text' OR LENGTH(region.value)<>2 OR region.value<>UPPER(region.value))<>0
);
INSERT INTO global_landing_pool_data_guard
SELECT COUNT(*) FROM landing_client_grants grant
LEFT JOIN landing_server_states server ON server.node_id=grant.landing_node_id
WHERE grant.status<>'revoked' AND server.node_id IS NULL;
INSERT INTO global_landing_pool_data_guard
SELECT COUNT(*) FROM landing_proxy_states proxy
LEFT JOIN landing_server_states server ON server.node_id=proxy.landing_node_id
WHERE json_extract(proxy.desired_json,'$.proxy') IS NOT NULL AND server.node_id IS NULL;
DROP TABLE global_landing_pool_data_guard;

INSERT INTO settings(key,value)
VALUES('three_x_ui_landing_selection','{"nodeIds":[],"revision":1}')
ON CONFLICT(key) DO NOTHING;

UPDATE settings
SET value=json_set(
 value,
 '$.landingRegionCodes', json(COALESCE((
   SELECT json_group_object(region.key,region.value)
   FROM settings policy, json_each(policy.value,'$.landingRegionCodes') region
   WHERE policy.key LIKE 'node-exits:%' AND policy.key NOT LIKE 'node-exits-error:%'
     AND EXISTS(SELECT 1 FROM json_each(settings.value,'$.nodeIds') selected WHERE selected.value=region.key)
 ),'{}')),
 '$.retiringNodeIds',json(COALESCE((
   SELECT json_group_array(node_id) FROM (
     SELECT DISTINCT landing_node_id AS node_id FROM landing_client_grants grant
     WHERE grant.status<>'revoked'
       AND NOT EXISTS(SELECT 1 FROM json_each(settings.value,'$.nodeIds') selected WHERE selected.value=grant.landing_node_id)
     UNION
     SELECT DISTINCT landing_node_id AS node_id FROM landing_proxy_states proxy
     WHERE json_extract(proxy.desired_json,'$.proxy') IS NOT NULL
       AND NOT EXISTS(SELECT 1 FROM json_each(settings.value,'$.nodeIds') selected WHERE selected.value=proxy.landing_node_id)
   )
 ),'[]'))
)
WHERE key='three_x_ui_landing_selection';

DELETE FROM settings WHERE key LIKE 'node-exits:%';
PRAGMA user_version = 79;

-- +goose Down
SELECT RAISE(ABORT, 'forward-only migration');
