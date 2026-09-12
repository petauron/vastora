-- +goose Up
-- No existing client receives a grant. Existing forced landing rows are kept.
CREATE TABLE three_x_ui_client_accounts (
 id TEXT PRIMARY KEY,
 controller_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
 email TEXT NOT NULL,
 metadata_json BLOB NOT NULL CHECK(json_valid(metadata_json)),
 mode TEXT NOT NULL DEFAULT 'fixed' CHECK(mode IN ('fixed','advanced','both')),
 revision INTEGER NOT NULL DEFAULT 1,
 pending_command_id TEXT NOT NULL DEFAULT '',
 managed_quota INTEGER NOT NULL DEFAULT 0 CHECK(managed_quota IN(0,1)),
 observed_at TEXT NOT NULL,
 UNIQUE(controller_id,email)
);
CREATE TABLE landing_client_capabilities (
 node_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 generation INTEGER NOT NULL,
 peer_json BLOB NOT NULL CHECK(json_valid(peer_json)),
 observed_at TEXT NOT NULL
);
CREATE TABLE landing_client_grants (
 id TEXT PRIMARY KEY,
 parent_id TEXT NOT NULL REFERENCES three_x_ui_client_accounts(id) ON DELETE RESTRICT,
 application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
 service_id TEXT NOT NULL REFERENCES services(id) ON DELETE RESTRICT,
 landing_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
 source_peer_json BLOB NOT NULL CHECK(json_valid(source_peer_json)),
 grant_json BLOB NOT NULL CHECK(json_valid(grant_json)),
 credential_secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
 material_secret_id TEXT REFERENCES secrets(id) ON DELETE RESTRICT,
 desired_revision INTEGER NOT NULL DEFAULT 1 CHECK(desired_revision>0),
 applied_revision INTEGER NOT NULL DEFAULT 0,
 route_revision INTEGER NOT NULL DEFAULT 0,
 status TEXT NOT NULL CHECK(status IN ('preparing','prepared','configuring','activating','ready','paused','revoking','revoked','failed')),
 last_error TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL,
 UNIQUE(parent_id,application_id,landing_node_id)
);
CREATE INDEX landing_client_grants_application ON landing_client_grants(application_id,status);
CREATE TABLE landing_client_blocks (
 parent_id TEXT NOT NULL REFERENCES three_x_ui_client_accounts(id) ON DELETE RESTRICT,
 application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
 inbound_tag TEXT NOT NULL,
 user_name TEXT NOT NULL,
 identity TEXT NOT NULL,
 PRIMARY KEY(parent_id,application_id,inbound_tag,user_name)
);
PRAGMA user_version = 73;
-- +goose Down
SELECT RAISE(ABORT, 'forward-only migration');
