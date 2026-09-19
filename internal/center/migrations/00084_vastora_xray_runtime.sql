-- +goose Up
-- Worker nodes are managed by Vastora's Xray runtime. Keep the real 3x-ui
-- controller identity unchanged and move only worker-owned service endpoints
-- and node-local REALITY routes to the dedicated runtime alias.
UPDATE services
SET endpoint = 'vastora-xray:' || container_port,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE application_id IN (
    SELECT id FROM applications
    WHERE app_key = 'vastora-official/3x-ui' AND runtime = 'docker' AND role = 'worker'
)
  AND endpoint LIKE 'vastora-3x-ui:%';

UPDATE node_listener_states
SET desired_json = json_set(
        desired_json,
        '$.revision', desired_revision + 1,
        '$.listener.routes', json((
            SELECT json_group_array(json(
                CASE
                    WHEN json_extract(route.value, '$.managedReality') = 1
                     AND EXISTS (
                         SELECT 1
                         FROM applications a
                         WHERE a.node_id = node_listener_states.node_id
                           AND a.app_key = 'vastora-official/3x-ui'
                           AND a.runtime = 'docker'
                           AND a.role = 'worker'
                     )
                    THEN json_set(route.value, '$.upstreams', json_array(json_object('address', 'vastora-xray', 'port', 443)))
                    ELSE route.value
                END
            ))
            FROM json_each(desired_json, '$.listener.routes') route
        ))
    ),
    desired_revision = desired_revision + 1,
    status = 'pending',
    attempt = 0,
    lease_expires_at = '',
    last_error = '',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE status <> 'stopped' AND EXISTS (
    SELECT 1 FROM json_each(desired_json, '$.listener.routes') route
    WHERE json_extract(route.value, '$.managedReality') = 1
      AND EXISTS (
          SELECT 1
          FROM applications a
          WHERE a.node_id = node_listener_states.node_id
            AND a.app_key = 'vastora-official/3x-ui'
            AND a.runtime = 'docker'
            AND a.role = 'worker'
      )
      AND (
          COALESCE(json_array_length(json_extract(route.value, '$.upstreams')), 0) <> 1
          OR COALESCE(json_extract(route.value, '$.upstreams[0].address'), '') <> 'vastora-xray'
          OR COALESCE(json_extract(route.value, '$.upstreams[0].port'), 0) <> 443
      )
);

PRAGMA user_version = 84;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
