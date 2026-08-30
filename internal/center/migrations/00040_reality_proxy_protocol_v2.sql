-- +goose Up
-- Existing routes must be withdrawn before HAProxy begins sending Proxy
-- Protocol headers. The normal REALITY hardening worker will update and read
-- back the managed Xray inbound before CreatePublication restores the route.
UPDATE three_x_ui_reality_guards
SET status = 'action_required',
    last_error = 'REALITY Proxy Protocol v2 upgrade requires hardening',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE status = 'ready';

UPDATE services
SET status = 'degraded',
    last_error = 'REALITY Proxy Protocol v2 upgrade requires hardening',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id IN (
    SELECT service_id FROM three_x_ui_reality_guards WHERE status = 'action_required'
) AND status <> 'stopped';

DELETE FROM routes
WHERE publication_id IN (
    SELECT publication.id
    FROM publications publication
    JOIN three_x_ui_reality_guards guard ON guard.service_id = publication.service_id
    WHERE guard.status = 'action_required'
);

UPDATE publications
SET status = 'stopped',
    last_error = 'REALITY guard requires hardening before publication',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE service_id IN (
    SELECT service_id FROM three_x_ui_reality_guards WHERE status = 'action_required'
) AND status <> 'stopped';

PRAGMA user_version = 40;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
