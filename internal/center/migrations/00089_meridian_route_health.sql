-- +goose Up
-- A successful Xray configuration receipt is not evidence that an entry can
-- reach its fixed landing peer directly. Do not inherit unproven health.
ALTER TABLE meridian_route_grants
 ADD COLUMN health_expires_unix_ms INTEGER NOT NULL DEFAULT 0 CHECK(health_expires_unix_ms>=0);

-- Entry identity is authorization, not a current capability observation. Only
-- an explicit route authorization or verified legacy import may establish it.
ALTER TABLE meridian_endpoints ADD COLUMN source_peer_json BLOB NOT NULL DEFAULT '{}';

UPDATE meridian_route_grants
SET runtime_healthy=0,
    status=CASE WHEN status='ready' THEN 'blocked' ELSE status END,
    last_error=CASE WHEN status='ready'
      THEN 'Awaiting fresh direct entry-to-landing runtime evidence.' ELSE last_error END
WHERE runtime_healthy=1;

PRAGMA user_version = 89;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
