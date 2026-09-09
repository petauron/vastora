-- +goose Up
CREATE TABLE cloudflare_access_settings (
    id INTEGER PRIMARY KEY CHECK(id = 1),
    session_duration TEXT NOT NULL DEFAULT '24h' CHECK(session_duration IN ('15m','30m','1h','6h','12h','24h','48h','72h','168h','720h')),
    sync_json TEXT NOT NULL DEFAULT '{"status":"not_synced","total":0,"updated":0}' CHECK(json_valid(sync_json))
);
INSERT INTO cloudflare_access_settings(id) VALUES(1);
PRAGMA user_version = 66;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
