-- +goose Up
-- Migration 56 reset shared-443 rows to pending even if an earlier security
-- migration had stopped them. Repair only unprotected REALITY publications;
-- healthy guards, unrelated routes and explicitly stopped entries are retained.
-- This staging table is populated before crossing released migration 56. For
-- installations already beyond that version there is no trustworthy way to
-- infer a past user stop from a pending row, so no such state is fabricated.
CREATE TABLE IF NOT EXISTS migration_56_stopped_publications (
    publication_id TEXT PRIMARY KEY, last_error TEXT NOT NULL,
    action_required INTEGER NOT NULL CHECK(action_required IN (0,1))
);
CREATE TABLE reality_quarantine_v64 (publication_id TEXT PRIMARY KEY);
INSERT INTO reality_quarantine_v64(publication_id)
SELECT p.id FROM publications p
JOIN services s ON s.id = p.service_id
JOIN applications a ON a.id = s.application_id
LEFT JOIN three_x_ui_reality_guards g ON g.service_id = s.id
WHERE a.app_key = 'vastora-official/3x-ui' AND s.app_protocol = 'vless/tcp/reality'
AND p.status <> 'stopped' AND COALESCE(g.status, '') <> 'ready';
INSERT OR IGNORE INTO reality_quarantine_v64(publication_id)
SELECT p.id FROM publications p JOIN migration_56_stopped_publications stopped ON stopped.publication_id = p.id
WHERE p.status <> 'stopped';

-- Remove stale route snapshots too: changing just publication metadata leaves
-- an old Agent task capable of restoring the forbidden public route.
CREATE TABLE reality_quarantine_routes_v64 (id TEXT PRIMARY KEY);
INSERT INTO reality_quarantine_routes_v64(id) SELECT publication_id FROM reality_quarantine_v64;
INSERT OR IGNORE INTO reality_quarantine_routes_v64(id)
SELECT id FROM routes WHERE publication_id IN (SELECT publication_id FROM reality_quarantine_v64);

UPDATE node_listener_states
SET desired_json = json_set(desired_json, '$.revision', desired_revision + 1, '$.listener.routes', json((
    SELECT json_group_array(json(r.value)) FROM json_each(desired_json, '$.listener.routes') r
    WHERE json_extract(r.value, '$.id') NOT IN (SELECT id FROM reality_quarantine_routes_v64)
))), desired_revision = desired_revision + 1, status = 'pending', lease_expires_at = '', last_error = '',
updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE EXISTS (SELECT 1 FROM json_each(desired_json, '$.listener.routes') r
    WHERE json_extract(r.value, '$.id') IN (SELECT id FROM reality_quarantine_routes_v64));

UPDATE gateway_states
SET desired_json = json_set(desired_json, '$.revision', desired_revision + 1, '$.sharedHttps.routes', json((
    SELECT json_group_array(json(r.value)) FROM json_each(desired_json, '$.sharedHttps.routes') r
    WHERE json_extract(r.value, '$.id') NOT IN (SELECT id FROM reality_quarantine_routes_v64)
))), desired_revision = desired_revision + 1, status = 'pending', lease_expires_at = '', last_error = '',
updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE EXISTS (SELECT 1 FROM json_each(desired_json, '$.sharedHttps.routes') r
    WHERE json_extract(r.value, '$.id') IN (SELECT id FROM reality_quarantine_routes_v64));

DELETE FROM routes WHERE publication_id IN (SELECT publication_id FROM reality_quarantine_v64);
DELETE FROM reality_security_checks WHERE publication_id IN (SELECT publication_id FROM reality_quarantine_v64);
UPDATE publications
SET status = 'stopped', action_required = 1, desired_revision = desired_revision + 1,
last_error = 'REALITY guard requires hardening before publication', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE id IN (SELECT publication_id FROM reality_quarantine_v64);
UPDATE publications
SET status = 'stopped',
action_required = (SELECT action_required FROM migration_56_stopped_publications stopped WHERE stopped.publication_id = publications.id),
last_error = (SELECT last_error FROM migration_56_stopped_publications stopped WHERE stopped.publication_id = publications.id)
WHERE id IN (SELECT publication_id FROM migration_56_stopped_publications);

DROP TABLE reality_quarantine_routes_v64;
DROP TABLE reality_quarantine_v64;
DROP TABLE migration_56_stopped_publications;
PRAGMA user_version = 64;

-- +goose Down
SELECT RAISE(ABORT, 'Vastora Center database downgrades are not supported');
