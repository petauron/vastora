-- +goose Up
CREATE TABLE publications_v4 (
    id TEXT PRIMARY KEY,
    service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK(kind IN ('lan_gateway', 'headscale_gateway', 'public_direct', 'public_shared_443', 'cloudflare_tunnel')),
    gateway_node_id TEXT REFERENCES agents(id) ON DELETE RESTRICT,
    hostname TEXT NOT NULL,
    dns_provider TEXT NOT NULL CHECK(dns_provider IN ('manual', 'cloudflare', 'headscale')),
    dns_record_id TEXT NOT NULL DEFAULT '',
    tls_enabled INTEGER NOT NULL DEFAULT 0,
    desired_revision INTEGER NOT NULL DEFAULT 1,
    applied_revision INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL CHECK(status IN ('pending', 'applying', 'ready', 'degraded', 'failed', 'stopped')),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(service_id, kind, hostname)
);

INSERT INTO publications_v4 (
    id, service_id, kind, gateway_node_id, hostname, dns_provider,
    dns_record_id, tls_enabled, desired_revision, applied_revision,
    status, last_error, created_at, updated_at
)
SELECT
    id, service_id, kind, gateway_node_id, hostname, dns_provider,
    dns_record_id, tls_enabled, desired_revision, applied_revision,
    status, last_error, created_at, updated_at
FROM publications;

CREATE TABLE routes_v4 (
    id TEXT PRIMARY KEY,
    publication_id TEXT NOT NULL REFERENCES publications_v4(id) ON DELETE CASCADE,
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

INSERT INTO routes_v4 (
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
ALTER TABLE publications_v4 RENAME TO publications;
ALTER TABLE routes_v4 RENAME TO routes;
PRAGMA user_version = 4;
