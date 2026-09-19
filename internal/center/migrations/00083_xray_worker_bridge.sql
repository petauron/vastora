-- +goose Up
-- Managed REALITY is strictly node-local. Replace the obsolete private host
-- socket with the Xray container alias and force every affected HAProxy state
-- through the normal desired/applied reconciliation path.
UPDATE services
SET endpoint = 'vastora-3x-ui:' || container_port,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE app_protocol = 'vless/tcp/reality'
  AND application_id IN (
      SELECT id FROM applications
      WHERE app_key = 'vastora-official/3x-ui' AND runtime = 'docker' AND role = 'worker'
  );

UPDATE node_listener_states
SET desired_json = json_set(
        desired_json,
        '$.revision', desired_revision + 1,
        '$.listener.routes', json((
            SELECT json_group_array(json(
                CASE
                    WHEN json_extract(route.value, '$.managedReality') = 1
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
      AND (
          COALESCE(json_array_length(json_extract(route.value, '$.upstreams')), 0) <> 1
          OR COALESCE(json_extract(route.value, '$.upstreams[0].address'), '') <> 'vastora-3x-ui'
          OR COALESCE(json_extract(route.value, '$.upstreams[0].port'), 0) <> 443
      )
);

PRAGMA user_version = 83;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
