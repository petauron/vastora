-- +goose Up
ALTER TABLE system_endpoint_aliases ADD COLUMN transition_id TEXT NOT NULL DEFAULT '';
ALTER TABLE system_endpoint_aliases ADD COLUMN lifecycle_state TEXT NOT NULL DEFAULT 'active' CHECK(lifecycle_state IN ('active', 'retiring', 'failed'));
ALTER TABLE system_endpoint_aliases ADD COLUMN retire_after TEXT NOT NULL DEFAULT '';
ALTER TABLE system_endpoint_aliases ADD COLUMN dns_account_id TEXT NOT NULL DEFAULT '';
ALTER TABLE system_endpoint_aliases ADD COLUMN dns_zone_id TEXT NOT NULL DEFAULT '';
ALTER TABLE system_endpoint_aliases ADD COLUMN dns_record_id TEXT NOT NULL DEFAULT '';
ALTER TABLE system_endpoint_aliases ADD COLUMN dns_record_type TEXT NOT NULL DEFAULT '';
ALTER TABLE system_endpoint_aliases ADD COLUMN dns_record_content TEXT NOT NULL DEFAULT '';
ALTER TABLE system_endpoint_aliases ADD COLUMN last_error TEXT NOT NULL DEFAULT '';

UPDATE system_endpoint_aliases
SET transition_id = created_at,
    retire_after = strftime('%Y-%m-%dT%H:%M:%fZ', datetime(created_at, '+30 days'));

PRAGMA user_version = 31;
