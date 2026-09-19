-- +goose Up
-- Generation 2 is the first Vastora-managed Xray worker runtime. Migration 84
-- moved listener endpoints before the matching application migration had
-- succeeded. Keep legacy workers on their live container identity until the
-- successful application completion atomically advances runtime_generation.
UPDATE services
SET endpoint = 'vastora-3x-ui:' || container_port,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE application_id IN (
    SELECT id FROM applications
    WHERE app_key = 'vastora-official/3x-ui'
      AND runtime = 'docker'
      AND role = 'worker'
      AND runtime_generation < 2
)
  AND endpoint LIKE 'vastora-xray:%';

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
                           AND a.runtime_generation < 2
                     )
                    THEN json_set(route.value, '$.upstreams', json_array(json_object('address', 'vastora-3x-ui', 'port', 443)))
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
      AND COALESCE(json_extract(route.value, '$.upstreams[0].address'), '') <> 'vastora-3x-ui'
      AND EXISTS (
          SELECT 1
          FROM applications a
          WHERE a.node_id = node_listener_states.node_id
            AND a.app_key = 'vastora-official/3x-ui'
            AND a.runtime = 'docker'
            AND a.role = 'worker'
            AND a.runtime_generation < 2
      )
);

PRAGMA user_version = 85;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
