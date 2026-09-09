-- +goose Up
-- Validate the released singleton setting before translating it. Malformed
-- state aborts the transaction; the existing migration runner backs up first.
CREATE TABLE landing_selection_migration_check (
    value TEXT NOT NULL CHECK(COALESCE(json_valid(value)
        AND json_type(value,'$.nodeId')='text'
        AND json_type(value,'$.revision')='integer'
        AND json_extract(value,'$.revision')>0,0))
);
INSERT INTO landing_selection_migration_check(value)
SELECT value FROM settings WHERE key='three_x_ui_landing_selection';
UPDATE settings SET value=json_object(
    'nodeIds',json(CASE WHEN json_extract(value,'$.nodeId')='' THEN '[]'
        ELSE json_array(json_extract(value,'$.nodeId')) END),
    'revision',json_extract(value,'$.revision'))
WHERE key='three_x_ui_landing_selection';
DROP TABLE landing_selection_migration_check;

CREATE TABLE landing_proxy_retirements (
    node_id TEXT NOT NULL REFERENCES landing_proxy_states(node_id) ON DELETE CASCADE,
    landing_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    source_address TEXT NOT NULL,
    PRIMARY KEY(node_id,landing_node_id,source_address)
);
PRAGMA user_version = 67;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
