-- +goose Up
-- Older Centers recorded an uncertain Meridian command as failed without
-- marking its endpoint and deployment failed. Expose that terminal state so
-- operators can inspect and recover it instead of seeing endless applying.
UPDATE meridian_deployments
SET status='failed',
    last_error=(SELECT command.error FROM application_commands command WHERE command.id=meridian_deployments.command_id),
    updated_at=(SELECT command.updated_at FROM application_commands command WHERE command.id=meridian_deployments.command_id)
WHERE status IN ('pending','applying')
  AND EXISTS(SELECT 1 FROM application_commands command
             WHERE command.id=meridian_deployments.command_id
               AND command.kind='meridian.runtime.apply'
               AND command.state='failed' AND command.reconciliation_required=1);

UPDATE meridian_endpoints
SET status='failed',runtime_healthy=0,
    last_error=(SELECT deployment.last_error FROM meridian_deployments deployment WHERE deployment.endpoint_id=meridian_endpoints.id),
    updated_at=(SELECT deployment.updated_at FROM meridian_deployments deployment WHERE deployment.endpoint_id=meridian_endpoints.id)
WHERE status IN ('pending','applying')
  AND EXISTS(SELECT 1 FROM meridian_deployments deployment JOIN application_commands command ON command.id=deployment.command_id
             WHERE deployment.endpoint_id=meridian_endpoints.id AND deployment.status='failed'
               AND command.kind='meridian.runtime.apply' AND command.state='failed' AND command.reconciliation_required=1);

UPDATE meridian_route_grants
SET status=CASE WHEN status IN ('revoking','revoked') THEN status ELSE 'failed' END,
    runtime_healthy=0,last_error='Meridian runtime execution needs explicit recovery'
WHERE status<>'revoked' AND endpoint_id IN
    (SELECT endpoint.id FROM meridian_endpoints endpoint JOIN meridian_deployments deployment ON deployment.endpoint_id=endpoint.id
     JOIN application_commands command ON command.id=deployment.command_id
     WHERE endpoint.status='failed' AND command.kind='meridian.runtime.apply'
       AND command.state='failed' AND command.reconciliation_required=1);

UPDATE meridian_cutover
SET last_error='Meridian runtime execution needs explicit recovery'
WHERE id=1 AND state IN ('project','verify','retire')
  AND EXISTS(SELECT 1 FROM meridian_endpoints endpoint JOIN meridian_deployments deployment ON deployment.endpoint_id=endpoint.id
             JOIN application_commands command ON command.id=deployment.command_id
             WHERE endpoint.status='failed' AND command.kind='meridian.runtime.apply'
               AND command.state='failed' AND command.reconciliation_required=1);

PRAGMA user_version = 94;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
