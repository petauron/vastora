-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;

CREATE TABLE node_listener_migration_cutovers (
    publication_id TEXT PRIMARY KEY,
    legacy_gateway_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    replacement_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    listener_ready INTEGER NOT NULL DEFAULT 0,
    publication_ready INTEGER NOT NULL DEFAULT 0,
    dns_reconciled INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE tunnel_connector_migration_cutovers (
    publication_id TEXT PRIMARY KEY,
    legacy_gateway_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    connector_reconciled INTEGER NOT NULL DEFAULT 0
);

INSERT INTO node_listener_migration_cutovers(publication_id, legacy_gateway_id, replacement_node_id)
SELECT p.id, p.gateway_node_id, a.node_id
FROM publications p
JOIN services s ON s.id = p.service_id
JOIN applications a ON a.id = s.application_id
WHERE p.kind = 'public_shared_443' AND p.status <> 'stopped' AND p.gateway_node_id IS NOT NULL;

INSERT INTO tunnel_connector_migration_cutovers(publication_id, legacy_gateway_id)
SELECT p.id, p.gateway_node_id
FROM publications p
WHERE p.kind = 'cloudflare_tunnel' AND p.status <> 'stopped' AND p.gateway_node_id IS NOT NULL
AND EXISTS(SELECT 1 FROM routes r WHERE r.publication_id = p.id);

CREATE TABLE publications_v56 (
    id TEXT PRIMARY KEY,
    service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK(kind IN ('lan_gateway', 'headscale_gateway', 'public_direct', 'public_shared_443', 'cloudflare_tunnel')),
    ingress_owner TEXT NOT NULL CHECK(ingress_owner IN ('site_gateway', 'application_node', 'tunnel_connector')),
    entry_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    hostname TEXT NOT NULL,
    sni_hostname TEXT NOT NULL DEFAULT '',
    dns_provider TEXT NOT NULL CHECK(dns_provider IN ('manual', 'cloudflare', 'headscale')),
    dns_record_id TEXT NOT NULL DEFAULT '',
    access_application_id TEXT NOT NULL DEFAULT '',
    tls_enabled INTEGER NOT NULL DEFAULT 0,
    desired_revision INTEGER NOT NULL DEFAULT 1,
    applied_revision INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL CHECK(status IN ('pending', 'applying', 'ready', 'degraded', 'failed', 'stopped')),
    last_error TEXT NOT NULL DEFAULT '',
    action_required INTEGER NOT NULL DEFAULT 0,
    cleanup_pending INTEGER NOT NULL DEFAULT 0,
    cleanup_attempt INTEGER NOT NULL DEFAULT 0,
    cleanup_retry_at TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK(
        (ingress_owner = 'site_gateway' AND kind IN ('lan_gateway', 'headscale_gateway', 'public_direct')) OR
        (ingress_owner = 'application_node' AND kind IN ('public_direct', 'public_shared_443')) OR
        (ingress_owner = 'tunnel_connector' AND kind = 'cloudflare_tunnel')
    ),
    UNIQUE(service_id, kind, hostname)
);

INSERT INTO publications_v56(
    id, service_id, kind, ingress_owner, entry_node_id, hostname, sni_hostname,
    dns_provider, dns_record_id, access_application_id, tls_enabled,
    desired_revision, applied_revision, status, last_error, action_required,
    cleanup_pending, cleanup_attempt, cleanup_retry_at, created_at, updated_at
)
SELECT
    p.id, p.service_id, p.kind,
    CASE
        WHEN p.kind = 'cloudflare_tunnel' THEN 'tunnel_connector'
        WHEN p.kind = 'public_shared_443' OR (p.kind = 'public_direct' AND s.protocol NOT IN ('http', 'https')) THEN 'application_node'
        ELSE 'site_gateway'
    END,
    CASE
        WHEN p.kind = 'public_shared_443' OR (p.kind = 'public_direct' AND s.protocol NOT IN ('http', 'https')) THEN a.node_id
        ELSE COALESCE(p.gateway_node_id, a.node_id)
    END,
    p.hostname, p.sni_hostname, p.dns_provider, p.dns_record_id,
    p.access_application_id, p.tls_enabled, p.desired_revision, p.applied_revision,
    CASE
        WHEN p.gateway_node_id IS NULL THEN 'stopped'
        WHEN (p.kind = 'public_shared_443' OR (p.kind = 'public_direct' AND s.protocol NOT IN ('http', 'https')))
          AND COALESCE(p.gateway_node_id, '') <> a.node_id THEN 'stopped'
        WHEN p.kind = 'public_shared_443' THEN 'pending'
        WHEN p.kind = 'cloudflare_tunnel' THEN 'pending'
        ELSE p.status
    END,
    CASE
        WHEN p.gateway_node_id IS NULL THEN 'migration requires action: legacy entry had no accepting node'
        WHEN (p.kind = 'public_shared_443' OR (p.kind = 'public_direct' AND s.protocol NOT IN ('http', 'https')))
          AND COALESCE(p.gateway_node_id, '') <> a.node_id THEN 'migration requires action: node-direct entry did not belong to its application node'
        ELSE p.last_error
    END,
    CASE
        WHEN p.gateway_node_id IS NULL THEN 1
        WHEN (p.kind = 'public_shared_443' OR (p.kind = 'public_direct' AND s.protocol NOT IN ('http', 'https')))
          AND COALESCE(p.gateway_node_id, '') <> a.node_id THEN 1
        ELSE 0
    END,
    p.cleanup_pending, p.cleanup_attempt, p.cleanup_retry_at, p.created_at, p.updated_at
FROM publications p
JOIN services s ON s.id = p.service_id
JOIN applications a ON a.id = s.application_id;

DROP TABLE publications;
ALTER TABLE publications_v56 RENAME TO publications;

CREATE TABLE node_listener_states (
    node_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    desired_revision INTEGER NOT NULL,
    applied_revision INTEGER NOT NULL DEFAULT 0,
    desired_json BLOB NOT NULL,
    status TEXT NOT NULL CHECK(status IN ('pending', 'applying', 'ready', 'failed', 'stopped')),
    attempt INTEGER NOT NULL DEFAULT 0,
    lease_expires_at TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);

INSERT INTO settings(key, value) VALUES('migration_56_node_local_listener', 'pending');
INSERT INTO settings(key, value) VALUES('migration_56_tunnel_connector', 'pending');

PRAGMA user_version = 56;
COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
