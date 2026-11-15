-- +goose Up
CREATE TABLE node_sites (
 application_id TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 hostname TEXT NOT NULL UNIQUE,
 port INTEGER NOT NULL CHECK(port BETWEEN 1024 AND 65535),
 revision INTEGER NOT NULL CHECK(revision>0),
 applied_revision INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL CHECK(state IN ('pending','issuing','ready','action_required','rolled_back')),
 sealed_key BLOB NOT NULL DEFAULT X'',
 certificate_pem TEXT NOT NULL DEFAULT '',
 fingerprint TEXT NOT NULL DEFAULT '',
 not_after TEXT NOT NULL DEFAULT '',
 ca_directory TEXT NOT NULL,
 error TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL
);
CREATE TABLE node_site_tasks (
 id TEXT PRIMARY KEY,
 application_id TEXT NOT NULL REFERENCES node_sites(application_id) ON DELETE CASCADE,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL,
 input_json TEXT NOT NULL CHECK(json_valid(input_json)),
 state TEXT NOT NULL CHECK(state IN ('pending','running','succeeded','failed')),
 attempt INTEGER NOT NULL DEFAULT 0,
 lease_expires_at TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 result_json TEXT NOT NULL DEFAULT '{}',
 updated_at TEXT NOT NULL
);
CREATE TABLE node_site_certmagic_storage (
 key TEXT PRIMARY KEY,
 sealed BLOB NOT NULL,
 modified_at TEXT NOT NULL,
 size INTEGER NOT NULL
);
PRAGMA user_version=127;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
