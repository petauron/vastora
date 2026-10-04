-- +goose Up
-- Never reinterpret an already queued artifact or move listeners while a
-- previous runtime task has an uncertain outcome. Open backs up before migration.
CREATE TEMP TABLE meridian_host_upgrade_guard (safe INTEGER NOT NULL CHECK(safe=1));
INSERT INTO meridian_host_upgrade_guard
 SELECT CASE WHEN EXISTS (
  SELECT 1 FROM application_commands
  WHERE kind='meridian.runtime.apply' AND (state IN ('pending','running') OR reconciliation_required=1)
 ) THEN 0 ELSE 1 END;
DROP TABLE meridian_host_upgrade_guard;

ALTER TABLE meridian_endpoints ADD COLUMN listen_address TEXT NOT NULL DEFAULT '';
UPDATE meridian_endpoints SET listen_address=COALESCE((
 SELECT profile.service_address FROM applications application
 JOIN agent_network_profiles profile ON profile.agent_id=application.node_id
 WHERE application.id=meridian_endpoints.application_id
),'');
UPDATE meridian_endpoints SET listen_port=10443;
UPDATE meridian_route_grants
 SET desired_revision=desired_revision+1,runtime_healthy=0,health_expires_unix_ms=0,status='pending',last_error=''
 WHERE enabled=1 AND status NOT IN ('revoked','revoking') AND endpoint_id IN (
  SELECT id FROM meridian_endpoints WHERE status='ready'
 );
UPDATE meridian_endpoints
 SET desired_revision=desired_revision+1,runtime_healthy=0,status='pending',last_error=''
 WHERE status='ready';
-- Existing services and HAProxy receipts stay unchanged until the new runtime
-- returns its exact receipt. Subscription identities are not regenerated.
PRAGMA user_version = 102;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
