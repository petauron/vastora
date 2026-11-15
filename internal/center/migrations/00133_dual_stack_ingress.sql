-- +goose Up
ALTER TABLE managed_domains ADD COLUMN secondary_address TEXT NOT NULL DEFAULT '';
ALTER TABLE managed_domains ADD COLUMN secondary_record_id TEXT NOT NULL DEFAULT '';
ALTER TABLE managed_domains ADD COLUMN secondary_expected_address TEXT NOT NULL DEFAULT '';
ALTER TABLE node_sites ADD COLUMN tls_loopback_only INTEGER NOT NULL DEFAULT 0 CHECK(tls_loopback_only IN (0,1));
PRAGMA user_version=133;
