-- +goose Up
CREATE TABLE center_remote_access (
    id INTEGER PRIMARY KEY CHECK(id = 1),
    hostname TEXT NOT NULL,
    audience_kind TEXT NOT NULL CHECK(audience_kind IN ('email', 'email_domain')),
    audience_value TEXT NOT NULL,
    otp_identity_provider_id TEXT NOT NULL DEFAULT '',
    access_application_id TEXT NOT NULL DEFAULT '',
    tunnel_id TEXT NOT NULL DEFAULT '',
    tunnel_token_secret_id TEXT REFERENCES secrets(id) ON DELETE RESTRICT,
    dns_record_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK(status IN ('pending', 'configured', 'failed')),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

PRAGMA user_version = 25;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
