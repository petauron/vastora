-- +goose Up
-- Changing the renderer requires new revisions, never rewriting an applied digest.
-- Fail closed if an older runtime command could still be delivered or executing.
CREATE TEMP TABLE meridian_domain_upgrade_guard (safe INTEGER NOT NULL CHECK(safe=1));
INSERT INTO meridian_domain_upgrade_guard
 SELECT CASE WHEN EXISTS (
  SELECT 1 FROM application_commands
  WHERE kind='meridian.runtime.apply' AND (state IN ('pending','running') OR reconciliation_required=1)
 ) THEN 0 ELSE 1 END;
DROP TABLE meridian_domain_upgrade_guard;

UPDATE meridian_route_grants
 SET desired_revision=desired_revision+1,runtime_healthy=0,health_expires_unix_ms=0,status='pending',last_error=''
 WHERE enabled=1 AND status NOT IN ('revoked','revoking') AND endpoint_id IN (
  SELECT id FROM meridian_endpoints WHERE vless_enabled=1 AND status='ready'
 );
UPDATE meridian_endpoints
 SET desired_revision=desired_revision+1,runtime_healthy=0,status='pending',last_error=''
 WHERE vless_enabled=1 AND status='ready';
PRAGMA user_version = 100;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
