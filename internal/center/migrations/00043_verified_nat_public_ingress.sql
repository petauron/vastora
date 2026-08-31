-- +goose Up
ALTER TABLE agent_network_profiles ADD COLUMN public_bind_address TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_network_profiles ADD COLUMN public_mode TEXT NOT NULL DEFAULT '' CHECK(public_mode IN ('', 'direct', 'nat'));
ALTER TABLE agent_network_profiles ADD COLUMN public_verified_at TEXT NOT NULL DEFAULT '';

UPDATE agent_network_profiles
SET public_bind_address = public_address,
    public_mode = 'direct',
    public_verified_at = confirmed_at
WHERE direct_public = 1;

PRAGMA user_version = 43;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
