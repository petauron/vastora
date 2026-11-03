-- +goose Up
CREATE TABLE agent_gateway_promotions (
 agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE RESTRICT,
 id TEXT NOT NULL UNIQUE,
 credential_hash BLOB NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('authorized','prepared','confirmed')),
 authorized_at TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 confirmed_at TEXT NOT NULL DEFAULT ''
);
PRAGMA user_version=129;
