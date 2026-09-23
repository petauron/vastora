-- +goose NO TRANSACTION
-- +goose Up
-- Center-owned subscription services must survive catalog and Agent inventory
-- reconciliation. Rebuild only the source constraint; preserve IDs and children.
PRAGMA foreign_keys = OFF;
PRAGMA legacy_alter_table = ON;
BEGIN IMMEDIATE;

CREATE TABLE services_v88 (
 id TEXT PRIMARY KEY,
 application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
 site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE RESTRICT,
 name TEXT NOT NULL,
 display_name TEXT NOT NULL DEFAULT '',
 region_code TEXT NOT NULL DEFAULT '',
 protocol TEXT NOT NULL CHECK(protocol IN ('http', 'https', 'tcp', 'udp')),
 container_port INTEGER NOT NULL,
 host_port INTEGER NOT NULL,
 endpoint TEXT NOT NULL,
 source TEXT NOT NULL CHECK(source IN ('catalog', 'observed', 'system')),
 app_protocol TEXT NOT NULL DEFAULT '',
 management INTEGER NOT NULL DEFAULT 0,
 observed_listen TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL CHECK(status IN ('pending', 'deploying', 'running', 'publishing', 'ready', 'degraded', 'failed', 'stopped')),
 last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(application_id, name)
);
INSERT INTO services_v88(rowid,id,application_id,site_id,name,display_name,region_code,protocol,container_port,host_port,endpoint,source,app_protocol,management,observed_listen,status,last_error,created_at,updated_at)
SELECT rowid,id,application_id,site_id,name,display_name,region_code,protocol,container_port,host_port,endpoint,source,app_protocol,management,observed_listen,status,last_error,created_at,updated_at FROM services;
DROP TABLE services;
ALTER TABLE services_v88 RENAME TO services;

-- Fail inside the transaction rather than commit a broken publication graph.
CREATE TEMP TABLE migration_88_integrity(valid INTEGER CHECK(valid=1));
INSERT INTO migration_88_integrity SELECT NOT EXISTS(SELECT 1 FROM pragma_foreign_key_check);
DROP TABLE migration_88_integrity;
PRAGMA user_version = 88;
COMMIT;
PRAGMA legacy_alter_table = OFF;
PRAGMA foreign_keys = ON;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
