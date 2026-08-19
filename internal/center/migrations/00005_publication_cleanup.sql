-- +goose Up
ALTER TABLE publications ADD COLUMN cleanup_pending INTEGER NOT NULL DEFAULT 0;
ALTER TABLE publications ADD COLUMN cleanup_attempt INTEGER NOT NULL DEFAULT 0;
ALTER TABLE publications ADD COLUMN cleanup_retry_at TEXT NOT NULL DEFAULT '';
PRAGMA user_version = 5;
