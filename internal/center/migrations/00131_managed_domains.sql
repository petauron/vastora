-- +goose Up
CREATE TABLE managed_domains (
 id TEXT PRIMARY KEY,
 application_id TEXT REFERENCES applications(id) ON DELETE SET NULL,
 agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
 resource_key TEXT NOT NULL,
 prefix TEXT NOT NULL DEFAULT '',
 hostname TEXT NOT NULL UNIQUE,
 address TEXT NOT NULL,
 record_type TEXT NOT NULL CHECK(record_type IN ('A','AAAA')),
 zone_id TEXT NOT NULL,
 record_id TEXT NOT NULL DEFAULT '',
 source_publication_id TEXT NOT NULL DEFAULT '',
 expected_record_address TEXT NOT NULL DEFAULT '',
 desired_revision INTEGER NOT NULL DEFAULT 1 CHECK(desired_revision>0),
 applied_revision INTEGER NOT NULL DEFAULT 0,
 status TEXT NOT NULL CHECK(status IN ('pending','ready','failed','retiring')),
 attempt INTEGER NOT NULL DEFAULT 0,
 retry_at TEXT NOT NULL DEFAULT '',
 last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 UNIQUE(application_id,resource_key)
);
CREATE INDEX managed_domains_pending ON managed_domains(status,retry_at,updated_at);
ALTER TABLE publications ADD COLUMN managed_domain_id TEXT NOT NULL DEFAULT '';
PRAGMA user_version=131;
