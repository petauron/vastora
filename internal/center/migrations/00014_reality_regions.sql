-- +goose Up
ALTER TABLE services ADD COLUMN region_code TEXT NOT NULL DEFAULT '';

UPDATE application_commands
SET state = 'failed',
    lease_expires_at = '',
    error = 'Center updated the REALITY naming format; retry and choose a region',
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE kind IN ('3xui.reality.create', '3xui.reality.rename')
  AND state IN ('pending', 'running');

PRAGMA user_version = 14;
