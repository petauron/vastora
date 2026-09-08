-- +goose Up
ALTER TABLE agents ADD COLUMN runtime_recovery TEXT NOT NULL DEFAULT '';
CREATE TABLE agent_network_profile_recovery (
    agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    public_key BLOB NOT NULL,
    profile_json BLOB NOT NULL CHECK(json_valid(profile_json))
);

PRAGMA user_version = 62;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
