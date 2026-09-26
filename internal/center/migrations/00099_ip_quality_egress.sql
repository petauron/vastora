-- +goose Up
CREATE TABLE ip_quality_checks_v99 (
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 id TEXT NOT NULL UNIQUE,
 address TEXT NOT NULL,
 bind_address TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','running','succeeded','failed')),
 attempt INTEGER NOT NULL DEFAULT 0,
 lease_expires_at TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 result_json TEXT NOT NULL DEFAULT 'null' CHECK(json_valid(result_json)),
 checked_at TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(agent_id,address)
);
INSERT INTO ip_quality_checks_v99(agent_id,id,address,bind_address,state,attempt,lease_expires_at,error,result_json,checked_at,created_at,updated_at)
 SELECT agent_id,id,address,bind_address,state,attempt,lease_expires_at,error,result_json,checked_at,created_at,updated_at FROM ip_quality_checks;
DROP TABLE ip_quality_checks;
ALTER TABLE ip_quality_checks_v99 RENAME TO ip_quality_checks;
CREATE UNIQUE INDEX ip_quality_active_agent_idx ON ip_quality_checks(agent_id) WHERE state IN ('pending','running');
PRAGMA user_version = 99;

-- +goose Down
SELECT RAISE(ABORT, 'center: database downgrades are not supported');
