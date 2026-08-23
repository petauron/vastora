-- +goose Up
CREATE TABLE site_certificates (
    site_id TEXT PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
    dns_names_json BLOB NOT NULL,
    secret_id TEXT REFERENCES secrets(id) ON DELETE SET NULL,
    not_after TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK(status IN ('pending', 'ready', 'failed')),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TEMP TABLE obsolete_publication_certificate_secrets AS
SELECT DISTINCT certificate_secret_id AS id
FROM publications
WHERE certificate_secret_id IS NOT NULL;

CREATE TABLE publications_v15 (
    id TEXT PRIMARY KEY,
    service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK(kind IN ('lan_gateway', 'headscale_gateway', 'public_direct', 'public_shared_443', 'cloudflare_tunnel')),
    gateway_node_id TEXT REFERENCES agents(id) ON DELETE RESTRICT,
    hostname TEXT NOT NULL,
    sni_hostname TEXT NOT NULL DEFAULT '',
    dns_provider TEXT NOT NULL CHECK(dns_provider IN ('manual', 'cloudflare', 'headscale')),
    dns_record_id TEXT NOT NULL DEFAULT '',
    tls_enabled INTEGER NOT NULL DEFAULT 0,
    desired_revision INTEGER NOT NULL DEFAULT 1,
    applied_revision INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL CHECK(status IN ('pending', 'applying', 'ready', 'degraded', 'failed', 'stopped')),
    last_error TEXT NOT NULL DEFAULT '',
    cleanup_pending INTEGER NOT NULL DEFAULT 0,
    cleanup_attempt INTEGER NOT NULL DEFAULT 0,
    cleanup_retry_at TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(service_id, kind, hostname)
);

INSERT INTO publications_v15 (
    id, service_id, kind, gateway_node_id, hostname, sni_hostname, dns_provider,
    dns_record_id, tls_enabled, desired_revision, applied_revision, status,
    last_error, cleanup_pending, cleanup_attempt, cleanup_retry_at, created_at, updated_at
)
SELECT
    id, service_id, kind, gateway_node_id, hostname, sni_hostname, dns_provider,
    dns_record_id, tls_enabled, desired_revision, applied_revision, status,
    last_error, cleanup_pending, cleanup_attempt, cleanup_retry_at, created_at, updated_at
FROM publications;

CREATE TABLE routes_v15 (
    id TEXT PRIMARY KEY,
    publication_id TEXT NOT NULL REFERENCES publications_v15(id) ON DELETE CASCADE,
    site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    gateway_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    hostname TEXT NOT NULL,
    protocol TEXT NOT NULL CHECK(protocol IN ('http', 'https')),
    upstreams_json BLOB NOT NULL,
    tls_enabled INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL CHECK(status IN ('pending', 'applying', 'ready', 'failed')),
    desired_revision INTEGER NOT NULL DEFAULT 0,
    applied_revision INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(publication_id, gateway_node_id)
);

INSERT INTO routes_v15 (
    id, publication_id, site_id, service_id, gateway_node_id, hostname,
    protocol, upstreams_json, tls_enabled, status, desired_revision,
    applied_revision, last_error, created_at, updated_at
)
SELECT
    id, publication_id, site_id, service_id, gateway_node_id, hostname,
    protocol, upstreams_json, tls_enabled, status, desired_revision,
    applied_revision, last_error, created_at, updated_at
FROM routes;

DROP TABLE routes;
DROP TABLE publications;
ALTER TABLE publications_v15 RENAME TO publications;
ALTER TABLE routes_v15 RENAME TO routes;

DELETE FROM secrets
WHERE id IN (SELECT id FROM obsolete_publication_certificate_secrets);
DROP TABLE obsolete_publication_certificate_secrets;

UPDATE application_commands
SET state = 'failed',
    lease_expires_at = '',
    error = 'Center updated the REALITY display-name format; retry the operation',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE kind IN ('3xui.reality.create', '3xui.reality.rename')
  AND state IN ('pending', 'running');

PRAGMA user_version = 15;
