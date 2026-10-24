-- +goose Up
ALTER TABLE sessions ADD COLUMN absolute_expires_at TEXT NOT NULL DEFAULT '';
-- Existing sessions keep their original deadline; never revive expired sessions.
UPDATE sessions SET absolute_expires_at = expires_at;
PRAGMA user_version = 119;
