-- +goose Up
-- Directional history begins at the first authenticated sample after upgrade.
-- Existing combined totals cannot be reliably split into upload/download.
CREATE TABLE meridian_line_usage (
 credential_id TEXT PRIMARY KEY REFERENCES meridian_credentials(id) ON DELETE CASCADE,
 upload_bytes INTEGER NOT NULL DEFAULT 0 CHECK(upload_bytes>=0),
 download_bytes INTEGER NOT NULL DEFAULT 0 CHECK(download_bytes>=0),
 started_at TEXT NOT NULL,
 observed_at TEXT NOT NULL
);
PRAGMA user_version = 108;
