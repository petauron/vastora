-- +goose Up
ALTER TABLE center_remote_access
ADD COLUMN protection_mode TEXT NOT NULL DEFAULT 'access'
CHECK(protection_mode IN ('access', 'native'));

ALTER TABLE center_remote_access
ADD COLUMN turnstile_site_key TEXT NOT NULL DEFAULT '';

ALTER TABLE center_remote_access
ADD COLUMN turnstile_secret_id TEXT REFERENCES secrets(id) ON DELETE RESTRICT;

CREATE TABLE login_failures (
    scope TEXT NOT NULL CHECK(scope IN ('account', 'client')),
    key_hash TEXT NOT NULL,
    failed_count INTEGER NOT NULL CHECK(failed_count >= 0),
    window_started_at TEXT NOT NULL,
    blocked_until TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(scope, key_hash)
);

CREATE INDEX login_failures_updated_idx ON login_failures(updated_at);

PRAGMA user_version = 60;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
