-- +goose Up
-- Existing runtime leases are not startup acceptance. Require one new
-- authenticated startup report before publishing these routes again.
-- Preserve configuration, credentials, usage, disabled and revoked states.
UPDATE meridian_route_grants
SET runtime_healthy=0, health_expires_unix_ms=0,
    status=CASE WHEN status='ready' THEN 'blocked' ELSE status END,
    last_error='Waiting for startup route acceptance after readiness upgrade'
WHERE enabled=1 AND status NOT IN ('revoked','revoking')
  AND (runtime_healthy<>0 OR health_expires_unix_ms>0);
PRAGMA user_version = 120;
