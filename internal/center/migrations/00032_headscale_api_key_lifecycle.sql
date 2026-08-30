-- +goose Up
CREATE TABLE headscale_api_keys (
    id INTEGER PRIMARY KEY CHECK(id = 1),
    key_id INTEGER NOT NULL DEFAULT 0,
    key_prefix TEXT NOT NULL DEFAULT '',
    expires_at TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK(state IN ('ready', 'preparing', 'committing')),
    previous_prefix TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);

PRAGMA user_version = 32;
