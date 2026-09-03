-- +goose Up
CREATE TABLE reality_security_checks (
    publication_id TEXT PRIMARY KEY REFERENCES publications(id) ON DELETE CASCADE,
    publication_revision INTEGER NOT NULL CHECK(publication_revision > 0),
    guard_revision INTEGER NOT NULL CHECK(guard_revision > 0),
    status TEXT NOT NULL CHECK(status IN ('safe', 'affected', 'inconclusive')),
    scope TEXT NOT NULL CHECK(scope IN ('remote', 'same_host')),
    checks_json BLOB NOT NULL CHECK(json_valid(checks_json)),
    requested_by TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
    checked_at TEXT NOT NULL
);

CREATE INDEX reality_security_checks_checked_idx ON reality_security_checks(checked_at DESC);

PRAGMA user_version = 61;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
