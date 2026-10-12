-- +goose Up
-- No table refers to grant IDs through a foreign key. Rebuild the grant table
-- to replace its single-exit uniqueness boundary without changing credentials,
-- quota watermarks, applied evidence, or the behavior of existing routes.
CREATE TABLE meridian_route_grants_v111 (
 id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES meridian_accounts(id) ON DELETE RESTRICT,
 endpoint_id TEXT NOT NULL REFERENCES meridian_endpoints(id) ON DELETE RESTRICT,
 egress_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
 ipv4_egress_node_id TEXT REFERENCES agents(id) ON DELETE RESTRICT,
 base_credential_id TEXT NOT NULL REFERENCES meridian_credentials(id) ON DELETE RESTRICT,
 route_credential_id TEXT NOT NULL UNIQUE REFERENCES meridian_credentials(id) ON DELETE RESTRICT,
 mode TEXT NOT NULL DEFAULT 'fixed' CHECK(mode='fixed'),
 hide_native INTEGER NOT NULL DEFAULT 0 CHECK(hide_native IN(0,1)),
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN(0,1)),
 desired_revision INTEGER NOT NULL DEFAULT 1 CHECK(desired_revision>0),
 applied_revision INTEGER NOT NULL DEFAULT 0 CHECK(applied_revision BETWEEN 0 AND desired_revision),
 runtime_healthy INTEGER NOT NULL DEFAULT 0 CHECK(runtime_healthy IN(0,1)),
 health_expires_unix_ms INTEGER NOT NULL DEFAULT 0 CHECK(health_expires_unix_ms>=0),
 status TEXT NOT NULL CHECK(status IN ('importing','pending','applying','ready','blocked','revoking','revoked','failed')),
 last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 CHECK(base_credential_id<>route_credential_id),
 CHECK(ipv4_egress_node_id IS NULL OR ipv4_egress_node_id<>egress_node_id)
);
INSERT INTO meridian_route_grants_v111
SELECT id,account_id,endpoint_id,egress_node_id,NULL,base_credential_id,route_credential_id,mode,hide_native,enabled,desired_revision,applied_revision,runtime_healthy,health_expires_unix_ms,status,last_error,created_at,updated_at
FROM meridian_route_grants;
DROP TABLE meridian_route_grants;
ALTER TABLE meridian_route_grants_v111 RENAME TO meridian_route_grants;
CREATE INDEX meridian_route_grants_endpoint ON meridian_route_grants(endpoint_id,status);
CREATE UNIQUE INDEX meridian_route_grants_identity ON meridian_route_grants(account_id,endpoint_id,egress_node_id,COALESCE(ipv4_egress_node_id,''));
PRAGMA user_version = 111;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
