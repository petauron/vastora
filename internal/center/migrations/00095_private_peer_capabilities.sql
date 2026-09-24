-- +goose Up
-- Private peer observations remain authoritative input for Meridian's source
-- pin and landing authorization. Rename the table before legacy client
-- cleanup so those routes do not lose their authenticated identity evidence.
ALTER TABLE landing_client_capabilities RENAME TO agent_private_peer_capabilities;
PRAGMA user_version = 95;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
