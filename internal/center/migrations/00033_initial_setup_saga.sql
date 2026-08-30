-- +goose Up
CREATE TABLE initial_setup_operations (
    id INTEGER PRIMARY KEY CHECK(id = 1),
    input_hash TEXT NOT NULL,
    phase TEXT NOT NULL CHECK(phase IN ('headscale', 'fixed_endpoint', 'remote_access', 'commit', 'completed')),
    site_id TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT NOT NULL DEFAULT ''
);

PRAGMA user_version = 33;
