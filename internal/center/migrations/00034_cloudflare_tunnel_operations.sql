-- +goose Up
CREATE TABLE cloudflare_tunnel_operations (
    agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE RESTRICT,
    account_id TEXT NOT NULL,
    operation_id TEXT NOT NULL UNIQUE,
    tunnel_name TEXT NOT NULL UNIQUE,
    tunnel_secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
    tunnel_id TEXT NOT NULL DEFAULT '',
    phase TEXT NOT NULL CHECK(phase IN ('intent', 'creating', 'created')),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

PRAGMA user_version = 34;
