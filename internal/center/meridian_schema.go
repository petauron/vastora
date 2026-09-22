package center

// meridianSchema is the authoritative access, subscription, quota, and Xray
// deployment model. Legacy 3x-ui tables are migration input only; once the
// explicit cutover is complete no Meridian operation reads or writes them.
const meridianSchema = `
CREATE TABLE meridian_cutover (
 id INTEGER PRIMARY KEY CHECK(id=1),
 state TEXT NOT NULL CHECK(state IN ('not_required','inspect','backup','import','publish','project','verify','retire','complete','failed')),
 legacy_controller_application_id TEXT REFERENCES applications(id) ON DELETE RESTRICT,
 backup_revision INTEGER NOT NULL DEFAULT 0 CHECK(backup_revision>=0),
 import_secret_id TEXT REFERENCES secrets(id) ON DELETE RESTRICT,
 import_sha256 TEXT NOT NULL DEFAULT '' CHECK(import_sha256='' OR (length(import_sha256)=64 AND import_sha256 NOT GLOB '*[^0-9a-f]*')),
 subscription_authority TEXT NOT NULL CHECK(subscription_authority IN ('legacy','meridian')),
 expected_accounts INTEGER NOT NULL DEFAULT 0 CHECK(expected_accounts>=0),
 expected_credentials INTEGER NOT NULL DEFAULT 0 CHECK(expected_credentials>=0),
 expected_endpoints INTEGER NOT NULL DEFAULT 0 CHECK(expected_endpoints>=0),
 expected_routes INTEGER NOT NULL DEFAULT 0 CHECK(expected_routes>=0),
 last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 switched_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE meridian_endpoints (
 id TEXT PRIMARY KEY,
 application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
 service_id TEXT NOT NULL REFERENCES services(id) ON DELETE RESTRICT,
 inbound_tag TEXT NOT NULL UNIQUE,
 listen_port INTEGER NOT NULL CHECK(listen_port BETWEEN 1 AND 65535),
 advertise_host TEXT NOT NULL,
 advertise_port INTEGER NOT NULL CHECK(advertise_port BETWEEN 1 AND 65535),
 target TEXT NOT NULL,
 target_ip TEXT NOT NULL,
 server_names_json BLOB NOT NULL CHECK(json_valid(server_names_json) AND json_type(server_names_json)='array'),
 private_key_secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
 public_key TEXT NOT NULL,
 short_ids_json BLOB NOT NULL CHECK(json_valid(short_ids_json) AND json_type(short_ids_json)='array'),
 fingerprint TEXT NOT NULL DEFAULT 'chrome',
 vless_enabled INTEGER NOT NULL DEFAULT 1 CHECK(vless_enabled IN(0,1)),
 hy2_enabled INTEGER NOT NULL DEFAULT 0 CHECK(hy2_enabled IN(0,1)),
 hy2_inbound_tag TEXT NOT NULL DEFAULT '',
 hy2_server_name TEXT NOT NULL DEFAULT '',
 hy2_certificate_secret_id TEXT REFERENCES secrets(id) ON DELETE RESTRICT,
 hy2_private_key_secret_id TEXT REFERENCES secrets(id) ON DELETE RESTRICT,
 hy2_certificate_not_after TEXT NOT NULL DEFAULT '',
 desired_revision INTEGER NOT NULL DEFAULT 1 CHECK(desired_revision>0),
 applied_revision INTEGER NOT NULL DEFAULT 0 CHECK(applied_revision BETWEEN 0 AND desired_revision),
 runtime_healthy INTEGER NOT NULL DEFAULT 0 CHECK(runtime_healthy IN(0,1)),
 legacy_retired INTEGER NOT NULL DEFAULT 0 CHECK(legacy_retired IN(0,1)),
 status TEXT NOT NULL CHECK(status IN ('importing','pending','applying','ready','failed','retired')),
 last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 source_peer_json BLOB NOT NULL DEFAULT '{}',
 UNIQUE(application_id),
 UNIQUE(service_id),
 CHECK(vless_enabled=1 OR hy2_enabled=1),
 CHECK(hy2_enabled=0 OR (hy2_inbound_tag<>'' AND hy2_server_name<>'' AND hy2_certificate_secret_id IS NOT NULL AND hy2_private_key_secret_id IS NOT NULL))
);
CREATE UNIQUE INDEX meridian_endpoints_hy2_tag ON meridian_endpoints(hy2_inbound_tag) WHERE hy2_inbound_tag<>'';
CREATE TABLE meridian_accounts (
 id TEXT PRIMARY KEY,
 display_name TEXT NOT NULL,
 total_bytes INTEGER NOT NULL DEFAULT 0 CHECK(total_bytes>=0),
 expiry_time INTEGER NOT NULL DEFAULT 0 CHECK(expiry_time>=0),
 reset_days INTEGER NOT NULL DEFAULT 0 CHECK(reset_days BETWEEN 0 AND 3650),
 next_reset_at TEXT NOT NULL DEFAULT '',
 last_reset_at TEXT NOT NULL DEFAULT '',
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN(0,1)),
 subscription_token_secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
 subscription_token_sha256 TEXT NOT NULL UNIQUE CHECK(length(subscription_token_sha256)=64 AND subscription_token_sha256 NOT GLOB '*[^0-9a-f]*'),
 desired_revision INTEGER NOT NULL DEFAULT 1 CHECK(desired_revision>0),
 applied_revision INTEGER NOT NULL DEFAULT 0 CHECK(applied_revision BETWEEN 0 AND desired_revision),
 status TEXT NOT NULL CHECK(status IN ('importing','active','disabled','expired','failed')),
 last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE TABLE meridian_credentials (
 id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES meridian_accounts(id) ON DELETE RESTRICT,
 endpoint_id TEXT NOT NULL REFERENCES meridian_endpoints(id) ON DELETE RESTRICT,
 kind TEXT NOT NULL CHECK(kind IN ('native','route')),
 user_name TEXT NOT NULL UNIQUE,
 identity_sha256 TEXT NOT NULL CHECK(length(identity_sha256)=64 AND identity_sha256 NOT GLOB '*[^0-9a-f]*'),
 protocol_secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
 hy2_auth_secret_id TEXT REFERENCES secrets(id) ON DELETE RESTRICT,
 hy2_identity_sha256 TEXT NOT NULL DEFAULT '' CHECK(hy2_identity_sha256='' OR (length(hy2_identity_sha256)=64 AND hy2_identity_sha256 NOT GLOB '*[^0-9a-f]*')),
 egress_node_id TEXT REFERENCES agents(id) ON DELETE RESTRICT,
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN(0,1)),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 CHECK((kind='native' AND egress_node_id IS NULL) OR (kind='route' AND egress_node_id IS NOT NULL)),
 CHECK(kind='native' OR (hy2_auth_secret_id IS NULL AND hy2_identity_sha256='')),
 CHECK((hy2_auth_secret_id IS NULL AND hy2_identity_sha256='') OR (hy2_auth_secret_id IS NOT NULL AND hy2_identity_sha256<>''))
);
CREATE INDEX meridian_credentials_account ON meridian_credentials(account_id,kind,enabled);
CREATE INDEX meridian_credentials_endpoint ON meridian_credentials(endpoint_id,enabled);
CREATE UNIQUE INDEX meridian_credentials_endpoint_identity ON meridian_credentials(endpoint_id,identity_sha256);
CREATE UNIQUE INDEX meridian_credentials_endpoint_hy2_identity ON meridian_credentials(endpoint_id,hy2_identity_sha256) WHERE hy2_identity_sha256<>'';
CREATE TABLE meridian_route_grants (
 id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES meridian_accounts(id) ON DELETE RESTRICT,
 endpoint_id TEXT NOT NULL REFERENCES meridian_endpoints(id) ON DELETE RESTRICT,
 egress_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
 base_credential_id TEXT NOT NULL REFERENCES meridian_credentials(id) ON DELETE RESTRICT,
 route_credential_id TEXT NOT NULL UNIQUE REFERENCES meridian_credentials(id) ON DELETE RESTRICT,
 mode TEXT NOT NULL DEFAULT 'fixed' CHECK(mode='fixed'),
 hide_native INTEGER NOT NULL DEFAULT 0 CHECK(hide_native IN(0,1)),
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN(0,1)),
 desired_revision INTEGER NOT NULL DEFAULT 1 CHECK(desired_revision>0),
 applied_revision INTEGER NOT NULL DEFAULT 0 CHECK(applied_revision BETWEEN 0 AND desired_revision),
 runtime_healthy INTEGER NOT NULL DEFAULT 0 CHECK(runtime_healthy IN(0,1)),
 health_expires_unix_ms INTEGER NOT NULL DEFAULT 0 CHECK(health_expires_unix_ms>=0),
 status TEXT NOT NULL CHECK(status IN ('importing','pending','applying','ready','blocked','revoking','revoked','failed')),
 last_error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 CHECK(base_credential_id<>route_credential_id),
 UNIQUE(account_id,endpoint_id,egress_node_id)
);
CREATE INDEX meridian_route_grants_endpoint ON meridian_route_grants(endpoint_id,status);
CREATE TABLE meridian_usage_watermarks (
 credential_id TEXT PRIMARY KEY REFERENCES meridian_credentials(id) ON DELETE CASCADE,
 baseline_bytes INTEGER NOT NULL DEFAULT 0 CHECK(baseline_bytes>=0),
 observed_bytes INTEGER NOT NULL DEFAULT 0 CHECK(observed_bytes>=baseline_bytes),
 raw_up_bytes INTEGER NOT NULL DEFAULT 0 CHECK(raw_up_bytes>=0),
 raw_down_bytes INTEGER NOT NULL DEFAULT 0 CHECK(raw_down_bytes>=0),
 observed_at TEXT NOT NULL
);
CREATE TABLE meridian_subscription_snapshots (
 account_id TEXT PRIMARY KEY REFERENCES meridian_accounts(id) ON DELETE CASCADE,
 secret_id TEXT NOT NULL UNIQUE REFERENCES secrets(id) ON DELETE RESTRICT,
 content_sha256 TEXT NOT NULL CHECK(length(content_sha256)=64 AND content_sha256 NOT GLOB '*[^0-9a-f]*'),
 account_revision INTEGER NOT NULL CHECK(account_revision>0),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE TRIGGER meridian_subscription_snapshots_delete_secret
AFTER DELETE ON meridian_subscription_snapshots
BEGIN DELETE FROM secrets WHERE id=OLD.secret_id; END;
CREATE INDEX meridian_accounts_reset ON meridian_accounts(next_reset_at) WHERE reset_days>0 AND status='active';
CREATE TABLE meridian_deployments (
 endpoint_id TEXT PRIMARY KEY REFERENCES meridian_endpoints(id) ON DELETE CASCADE,
 desired_revision INTEGER NOT NULL CHECK(desired_revision>0),
 applied_revision INTEGER NOT NULL DEFAULT 0 CHECK(applied_revision BETWEEN 0 AND desired_revision),
 desired_sha256 TEXT NOT NULL CHECK(length(desired_sha256)=64 AND desired_sha256 NOT GLOB '*[^0-9a-f]*'),
 applied_sha256 TEXT NOT NULL DEFAULT '' CHECK(applied_sha256='' OR (length(applied_sha256)=64 AND applied_sha256 NOT GLOB '*[^0-9a-f]*')),
 command_id TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL CHECK(status IN ('pending','applying','ready','failed')),
 last_error TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL
);
CREATE INDEX meridian_deployments_status ON meridian_deployments(status,updated_at);
INSERT INTO meridian_cutover(id,state,subscription_authority,created_at,updated_at)
VALUES(1,'not_required','meridian',strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now'));
CREATE TRIGGER application_commands_block_during_meridian_cutover
BEFORE INSERT ON application_commands
WHEN NEW.kind GLOB '3xui.*'
AND EXISTS(SELECT 1 FROM meridian_cutover WHERE id=1 AND state IN ('backup','import','publish','project','verify','retire'))
BEGIN SELECT RAISE(ABORT, 'Meridian authority cutover is in progress'); END;
CREATE TRIGGER application_command_updates_block_during_meridian_cutover
BEFORE UPDATE OF application_id,kind,input_json,state,reconciliation_required ON application_commands
WHEN NEW.kind GLOB '3xui.*'
AND (NEW.state IN ('pending','running') OR NEW.reconciliation_required=1)
AND EXISTS(SELECT 1 FROM meridian_cutover WHERE id=1 AND state IN ('backup','import','publish','project','verify','retire'))
BEGIN SELECT RAISE(ABORT, 'Meridian authority cutover is in progress'); END;
CREATE TRIGGER deployments_block_during_meridian_cutover
BEFORE INSERT ON deployments
WHEN NEW.app_key='vastora-official/3x-ui'
AND EXISTS(SELECT 1 FROM meridian_cutover WHERE id=1 AND state IN ('backup','import','publish','project','verify','retire'))
BEGIN SELECT RAISE(ABORT, 'Meridian authority cutover is in progress'); END;
CREATE TRIGGER deployment_updates_block_during_meridian_cutover
BEFORE UPDATE OF application_id,app_key,state,reconciliation_required ON deployments
WHEN NEW.app_key='vastora-official/3x-ui'
AND (NEW.state IN ('pending','running') OR NEW.reconciliation_required=1)
AND EXISTS(SELECT 1 FROM meridian_cutover WHERE id=1 AND state IN ('backup','import','publish','project','verify','retire'))
BEGIN SELECT RAISE(ABORT, 'Meridian authority cutover is in progress'); END;
`
