package center

// osReinstallSchema stores the single-use operation and its encrypted
// off-host recovery material. It never authorizes an install by itself.
const osReinstallSchema = `CREATE TABLE os_reinstall_tasks (
 id TEXT PRIMARY KEY,
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
 actor_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
 inspection_id TEXT NOT NULL,
 input_json TEXT NOT NULL CHECK(json_valid(input_json)),
 sealed_password BLOB NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('pending','running','backup_ready','armed','succeeded','failed','unknown','cancelled')),
 attempt INTEGER NOT NULL DEFAULT 0 CHECK(attempt IN (0,1)),
 lease_expires_at TEXT NOT NULL DEFAULT '',
 encrypted_backup BLOB NOT NULL DEFAULT X'',
 encrypted_access BLOB NOT NULL DEFAULT X'',
 backup_digest TEXT NOT NULL DEFAULT '',
 prepared_digest TEXT NOT NULL DEFAULT '',
 sealed_network BLOB NOT NULL DEFAULT X'',
 last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX os_reinstall_tasks_one_active ON os_reinstall_tasks(agent_id)
 WHERE state NOT IN ('succeeded','cancelled');
`
