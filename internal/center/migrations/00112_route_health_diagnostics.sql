-- +goose Up
-- Diagnostic evidence does not replace or extend any route's health lease.
ALTER TABLE meridian_route_grants ADD COLUMN health_diagnostics_json BLOB NOT NULL DEFAULT '{}'
 CHECK(json_valid(health_diagnostics_json) AND json_type(health_diagnostics_json)='object' AND length(health_diagnostics_json)<=16384);
CREATE TABLE meridian_route_health_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 route_id TEXT NOT NULL REFERENCES meridian_route_grants(id) ON DELETE CASCADE,
 recorded_at TEXT NOT NULL,
 fingerprint TEXT NOT NULL CHECK(length(fingerprint)=64),
 diagnostics_json BLOB NOT NULL CHECK(json_valid(diagnostics_json) AND length(diagnostics_json)<=16384)
);
CREATE INDEX meridian_route_health_events_route ON meridian_route_health_events(route_id,id DESC);
CREATE INDEX meridian_route_health_events_recorded ON meridian_route_health_events(recorded_at);
CREATE INDEX meridian_route_health_expiry ON meridian_route_grants(health_expires_unix_ms) WHERE status='ready' AND enabled=1;
PRAGMA user_version = 112;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
