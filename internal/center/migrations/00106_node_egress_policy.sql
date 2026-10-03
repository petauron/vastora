-- +goose Up
-- Opt-in only: absent rows preserve existing automatic egress and runtime.
CREATE TABLE node_egress_policies (
 node_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 policy TEXT NOT NULL CHECK(policy IN ('auto','ipv4_only','ipv6_only')),
 revision INTEGER NOT NULL CHECK(revision>0),
 verified_revision INTEGER NOT NULL DEFAULT 0 CHECK(verified_revision>=0),
 applied_policy TEXT NOT NULL DEFAULT 'auto' CHECK(applied_policy IN ('auto','ipv4_only','ipv6_only')),
 verified_json BLOB NOT NULL DEFAULT '{}' CHECK(json_valid(verified_json)),
 updated_at TEXT NOT NULL
);
PRAGMA user_version = 106;
