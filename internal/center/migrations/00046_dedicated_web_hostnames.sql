-- +goose Up
-- Shared Web hostnames with random path prefixes cannot transparently proxy
-- applications that own absolute asset, API, redirect, cookie, or WebSocket
-- paths. Retire those entries so the operator can recreate them with the new
-- dedicated application-and-node hostname contract. External resources stay
-- attached to one retired row and are removed by the durable cleanup worker.
CREATE TABLE retired_shared_publication_gateways (
    gateway_node_id TEXT PRIMARY KEY
);

INSERT INTO retired_shared_publication_gateways(gateway_node_id)
SELECT DISTINCT gateway_node_id
FROM publications
WHERE path_prefix <> '' AND gateway_node_id IS NOT NULL;

UPDATE publications
SET dns_record_id = ''
WHERE path_prefix <> ''
  AND dns_record_id <> ''
  AND id <> (
      SELECT MIN(other.id)
      FROM publications AS other
      WHERE other.path_prefix <> ''
        AND other.dns_record_id = publications.dns_record_id
  );

UPDATE publications
SET access_application_id = ''
WHERE path_prefix <> ''
  AND access_application_id <> ''
  AND id <> (
      SELECT MIN(other.id)
      FROM publications AS other
      WHERE other.path_prefix <> ''
        AND other.access_application_id = publications.access_application_id
  );

UPDATE publications
SET status = 'stopped',
    desired_revision = desired_revision + 1,
    cleanup_pending = CASE
        WHEN dns_record_id <> '' OR access_application_id <> '' OR kind = 'cloudflare_tunnel' OR dns_provider = 'headscale' THEN 1
        ELSE 0
    END,
    cleanup_attempt = 0,
    cleanup_retry_at = '',
    last_error = ''
WHERE path_prefix <> '' AND status <> 'stopped';

UPDATE publications
SET cleanup_pending = CASE
        WHEN dns_record_id <> '' OR access_application_id <> '' OR kind = 'cloudflare_tunnel' OR dns_provider = 'headscale' THEN 1
        ELSE 0
    END,
    cleanup_attempt = 0,
    cleanup_retry_at = ''
WHERE path_prefix <> '' AND status = 'stopped';

DELETE FROM routes
WHERE publication_id IN (
    SELECT id FROM publications WHERE path_prefix <> ''
);

DROP INDEX publications_hostname_path_idx;
ALTER TABLE routes DROP COLUMN path_prefix;
ALTER TABLE publications DROP COLUMN path_prefix;

PRAGMA user_version = 46;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
