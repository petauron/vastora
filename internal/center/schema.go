package center

import (
	"context"
	"fmt"
	"time"
)

const centerSchemaVersion = 39

func (s *Store) initializeSchema(ctx context.Context, existing bool) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA journal_mode = WAL`); err != nil {
		return fmt.Errorf("center: enable WAL: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("center: enable foreign keys: %w", err)
	}
	if existing {
		return s.migrateSchema(ctx)
	}
	if err := s.initializeCurrentSchema(ctx); err != nil {
		return err
	}
	return s.initializeMigrationHistory(ctx, centerSchemaVersion)
}

func (s *Store) initializeCurrentSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("center: begin schema initialization: %w", err)
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE storage_key_binding (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			sealed BLOB NOT NULL
		)`,
		`CREATE TABLE secret_deliveries (
			kind TEXT NOT NULL CHECK(kind IN ('deployment_credentials', 'application_command_result')),
			owner_id TEXT NOT NULL,
			operation_key_hash BLOB NOT NULL,
			request_hash BLOB NOT NULL,
			resource_id TEXT NOT NULL,
			state TEXT NOT NULL CHECK(state IN ('pending', 'acknowledged')),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(kind, owner_id, operation_key_hash),
			UNIQUE(kind, resource_id)
		)`,
		`CREATE TABLE secrets (
			id TEXT PRIMARY KEY,
			sealed BLOB NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE initial_setup_operations (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			input_hash TEXT NOT NULL,
			phase TEXT NOT NULL CHECK(phase IN ('headscale', 'fixed_endpoint', 'remote_access', 'commit', 'completed')),
			site_id TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			completed_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE admins (
			id TEXT PRIMARY KEY,
			username TEXT NOT NULL COLLATE NOCASE UNIQUE,
			password_hash TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE sessions (
			token_hash TEXT PRIMARY KEY,
			csrf_token TEXT NOT NULL,
			admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
			expires_at TEXT NOT NULL
		)`,
		`CREATE TABLE catalog_sources (
			id TEXT PRIMARY KEY,
			display_name TEXT NOT NULL,
			url TEXT NOT NULL,
			public_key BLOB NOT NULL,
			bearer_secret_id TEXT REFERENCES secrets(id),
			custom_ca BLOB,
			enabled INTEGER NOT NULL DEFAULT 1,
			refresh_seconds INTEGER NOT NULL,
			generation TEXT NOT NULL DEFAULT '',
			revision INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL,
			last_checked_at TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE catalog_cache (
			source_id TEXT PRIMARY KEY REFERENCES catalog_sources(id) ON DELETE CASCADE,
			envelope BLOB NOT NULL,
			etag TEXT NOT NULL DEFAULT '',
			last_modified TEXT NOT NULL DEFAULT '',
			fetched_at TEXT NOT NULL
		)`,
		`CREATE TABLE catalog_manifest_history (
			source_id TEXT NOT NULL,
			app_id TEXT NOT NULL,
			version TEXT NOT NULL,
			manifest_sha256 TEXT NOT NULL,
			first_seen_at TEXT NOT NULL,
			PRIMARY KEY(source_id, app_id, version)
		)`,
		`CREATE TABLE registry_credentials (
			id TEXT PRIMARY KEY,
			host TEXT NOT NULL,
			username TEXT NOT NULL,
			secret_id TEXT NOT NULL REFERENCES secrets(id),
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE organizations (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE sites (
			id TEXT PRIMARY KEY,
			organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE RESTRICT,
			name TEXT NOT NULL,
			code TEXT NOT NULL UNIQUE,
			description TEXT NOT NULL DEFAULT '',
			timezone TEXT NOT NULL,
			domain_suffix TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL CHECK(status IN ('active', 'disabled')),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE agent_enrollment_tokens (
			token_hash BLOB PRIMARY KEY,
			site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			center_url TEXT NOT NULL,
			roles_json BLOB NOT NULL,
			capabilities_json BLOB NOT NULL,
			bootstrap_secret_id TEXT REFERENCES secrets(id) ON DELETE SET NULL,
			ca_fingerprint TEXT NOT NULL DEFAULT '',
			expires_at TEXT NOT NULL,
			used_at TEXT
		)`,
		`CREATE TABLE agents (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			credential_hash BLOB NOT NULL UNIQUE,
			x25519_public_key BLOB NOT NULL DEFAULT X'',
			credential_revoked_at TEXT NOT NULL DEFAULT '',
			version TEXT NOT NULL,
			operating_system TEXT NOT NULL DEFAULT 'linux' CHECK(operating_system = 'linux'),
			architecture TEXT NOT NULL DEFAULT 'amd64' CHECK(architecture IN ('amd64', 'arm64')),
			status TEXT NOT NULL CHECK(status IN ('active', 'disabled')),
			applied_installations INTEGER NOT NULL DEFAULT 0,
			enrolled_at TEXT NOT NULL,
			last_seen_at TEXT NOT NULL,
			site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE RESTRICT,
			roles_json BLOB NOT NULL DEFAULT '[]',
			capabilities_json BLOB NOT NULL DEFAULT '{}',
			gateway_healthy INTEGER NOT NULL DEFAULT 0,
			runtime_generation INTEGER NOT NULL DEFAULT 0,
			tailscale_ownership TEXT NOT NULL DEFAULT '' CHECK(tailscale_ownership IN ('', 'managed', 'external'))
		)`,
		`CREATE TABLE agent_enrollment_operations (
			token_hash BLOB PRIMARY KEY REFERENCES agent_enrollment_tokens(token_hash) ON DELETE CASCADE,
			operation_id TEXT NOT NULL UNIQUE,
			request_hash BLOB NOT NULL,
			agent_id TEXT NOT NULL UNIQUE REFERENCES agents(id) ON DELETE CASCADE,
			response_secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
			expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TRIGGER agent_enrollment_operation_secret_cleanup
		AFTER DELETE ON agent_enrollment_operations
		BEGIN
			DELETE FROM secrets WHERE id = OLD.response_secret_id;
		END`,
		`CREATE TABLE site_gateways (
			site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
			agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			created_at TEXT NOT NULL,
			PRIMARY KEY(site_id, agent_id)
		)`,
		`CREATE TABLE agent_network_candidates (
			agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			address TEXT NOT NULL,
			interface_name TEXT NOT NULL,
			kind TEXT NOT NULL CHECK(kind IN ('lan', 'headscale', 'public')),
			observed_at TEXT NOT NULL,
			PRIMARY KEY(agent_id, address)
		)`,
		`CREATE TABLE agent_network_profiles (
			agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
			service_address TEXT NOT NULL,
			lan_address TEXT NOT NULL DEFAULT '',
			headscale_address TEXT NOT NULL DEFAULT '',
			public_address TEXT NOT NULL DEFAULT '',
			enabled_kinds_json BLOB NOT NULL,
			direct_public INTEGER NOT NULL DEFAULT 0,
			confirmed_at TEXT NOT NULL,
			candidate_observed_at TEXT NOT NULL
		)`,
		`CREATE TABLE agent_decommissions (
			agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
			delete_data INTEGER NOT NULL CHECK(delete_data IN (0, 1)),
			state TEXT NOT NULL CHECK(state IN ('pending', 'running', 'cleaning', 'succeeded', 'failed', 'abandoned')),
			attempt INTEGER NOT NULL DEFAULT 0,
			lease_expires_at TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE applications (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
			site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE RESTRICT,
			app_key TEXT NOT NULL,
			image TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL CHECK(status IN ('pending', 'deploying', 'running', 'failed', 'stopped')),
			runtime TEXT NOT NULL DEFAULT 'docker',
			runtime_generation INTEGER NOT NULL DEFAULT 0,
			role TEXT NOT NULL DEFAULT '' CHECK(role IN ('', 'master', 'worker')),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(node_id, app_key)
		)`,
		`CREATE UNIQUE INDEX applications_one_three_x_ui_master_idx ON applications(site_id) WHERE app_key = 'vastora-official/3x-ui' AND role = 'master' AND status IN ('pending', 'deploying', 'running')`,
		`CREATE TABLE three_x_ui_nodes (
			worker_application_id TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
			master_application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
			remote_node_id INTEGER,
			status TEXT NOT NULL CHECK(status IN ('pending', 'applying', 'ready', 'failed', 'stopped')),
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			CHECK(worker_application_id <> master_application_id),
			UNIQUE(master_application_id, remote_node_id)
		)`,
		`CREATE TABLE three_x_ui_backups (
			application_id TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
			revision INTEGER NOT NULL DEFAULT 0,
			state TEXT NOT NULL CHECK(state IN ('pending', 'ready', 'failed')),
			sealed BLOB,
			sha256 TEXT NOT NULL DEFAULT '',
			size INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE three_x_ui_migrations (
			id TEXT PRIMARY KEY,
			site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
			source_application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
			target_application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
			backup_revision INTEGER NOT NULL DEFAULT 0,
			state TEXT NOT NULL CHECK(state IN ('backing_up', 'restoring', 'switching', 'ready', 'failed')),
			step TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			CHECK(source_application_id <> target_application_id)
		)`,
		`CREATE UNIQUE INDEX three_x_ui_migrations_one_active_idx ON three_x_ui_migrations(site_id) WHERE state IN ('backing_up', 'restoring', 'switching')`,
		`CREATE TABLE services (
			id TEXT PRIMARY KEY,
			application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
			site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE RESTRICT,
			name TEXT NOT NULL,
			display_name TEXT NOT NULL DEFAULT '',
			region_code TEXT NOT NULL DEFAULT '',
			protocol TEXT NOT NULL CHECK(protocol IN ('http', 'https', 'tcp', 'udp')),
			container_port INTEGER NOT NULL,
			host_port INTEGER NOT NULL,
			endpoint TEXT NOT NULL,
			source TEXT NOT NULL CHECK(source IN ('catalog', 'observed')),
			app_protocol TEXT NOT NULL DEFAULT '',
			management INTEGER NOT NULL DEFAULT 0,
			observed_listen TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL CHECK(status IN ('pending', 'deploying', 'running', 'publishing', 'ready', 'degraded', 'failed', 'stopped')),
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(application_id, name)
		)`,
		`CREATE TABLE three_x_ui_inbound_plans (
			service_id TEXT PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
			inbound_tag TEXT NOT NULL,
			total_bytes INTEGER NOT NULL DEFAULT 0 CHECK(total_bytes >= 0),
			reset_days INTEGER NOT NULL DEFAULT 0 CHECK(reset_days >= 0),
			next_reset_at TEXT NOT NULL DEFAULT '',
			last_reset_at TEXT NOT NULL DEFAULT '',
			revision INTEGER NOT NULL DEFAULT 1 CHECK(revision > 0),
			status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active', 'resetting', 'failed')),
			retry_at TEXT NOT NULL DEFAULT '',
			attempt INTEGER NOT NULL DEFAULT 0 CHECK(attempt >= 0),
			last_error TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX three_x_ui_inbound_plans_due_idx ON three_x_ui_inbound_plans(status, next_reset_at, retry_at) WHERE reset_days > 0`,
		`CREATE TABLE three_x_ui_reality_guards (
			service_id TEXT PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
			target_host TEXT NOT NULL,
			target_ip TEXT NOT NULL,
			server_name TEXT NOT NULL,
			node_asn INTEGER NOT NULL DEFAULT 0 CHECK(node_asn >= 0),
			target_asn INTEGER NOT NULL DEFAULT 0 CHECK(target_asn >= 0),
			cdn_provider TEXT NOT NULL DEFAULT '',
			companion_inbound_id INTEGER NOT NULL DEFAULT 0 CHECK(companion_inbound_id >= 0),
			companion_tag TEXT NOT NULL,
			companion_port INTEGER NOT NULL DEFAULT 0 CHECK(companion_port = 0 OR companion_port BETWEEN 21000 AND 21031),
			revision INTEGER NOT NULL DEFAULT 1 CHECK(revision > 0),
			status TEXT NOT NULL CHECK(status IN ('pending', 'hardening', 'ready', 'action_required')),
			verified_at TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX three_x_ui_reality_guards_status_idx ON three_x_ui_reality_guards(status, updated_at)`,
		`CREATE TABLE publications (
			id TEXT PRIMARY KEY,
			service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
			kind TEXT NOT NULL CHECK(kind IN ('lan_gateway', 'headscale_gateway', 'public_direct', 'public_shared_443', 'cloudflare_tunnel')),
			gateway_node_id TEXT REFERENCES agents(id) ON DELETE RESTRICT,
			hostname TEXT NOT NULL,
			sni_hostname TEXT NOT NULL DEFAULT '',
			dns_provider TEXT NOT NULL CHECK(dns_provider IN ('manual', 'cloudflare', 'headscale')),
			dns_record_id TEXT NOT NULL DEFAULT '',
			tls_enabled INTEGER NOT NULL DEFAULT 0,
			desired_revision INTEGER NOT NULL DEFAULT 1,
			applied_revision INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL CHECK(status IN ('pending', 'applying', 'ready', 'degraded', 'failed', 'stopped')),
			last_error TEXT NOT NULL DEFAULT '',
			cleanup_pending INTEGER NOT NULL DEFAULT 0,
			cleanup_attempt INTEGER NOT NULL DEFAULT 0,
			cleanup_retry_at TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(service_id, kind, hostname)
		)`,
		`CREATE TABLE certificate_authorities (
			id TEXT PRIMARY KEY,
			account_uri TEXT NOT NULL,
			secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE site_certificates (
			site_id TEXT PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
			dns_names_json BLOB NOT NULL,
			secret_id TEXT REFERENCES secrets(id) ON DELETE SET NULL,
			not_after TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL CHECK(status IN ('pending', 'ready', 'failed')),
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE routes (
			id TEXT PRIMARY KEY,
			publication_id TEXT NOT NULL REFERENCES publications(id) ON DELETE CASCADE,
			site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
			service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
			gateway_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			hostname TEXT NOT NULL,
			protocol TEXT NOT NULL CHECK(protocol IN ('http', 'https')),
			upstreams_json BLOB NOT NULL,
			tls_enabled INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL CHECK(status IN ('pending', 'applying', 'ready', 'failed')),
			desired_revision INTEGER NOT NULL DEFAULT 0,
			applied_revision INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(publication_id, gateway_node_id)
		)`,
		`CREATE TABLE gateway_components (
			gateway_node_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
			desired_status TEXT NOT NULL CHECK(desired_status IN ('running', 'stopped')),
			generation INTEGER NOT NULL,
			applied_generation INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL CHECK(status IN ('pending', 'applying', 'ready', 'failed', 'stopped')),
			attempt INTEGER NOT NULL DEFAULT 0,
			lease_expires_at TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE gateway_states (
			gateway_node_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
			desired_revision INTEGER NOT NULL,
			applied_revision INTEGER NOT NULL DEFAULT 0,
			desired_json BLOB NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('pending', 'applying', 'ready', 'failed')),
			attempt INTEGER NOT NULL DEFAULT 0,
			lease_expires_at TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE network_integrations (
			kind TEXT PRIMARY KEY CHECK(kind IN ('cloudflare', 'headscale')),
			mode TEXT NOT NULL DEFAULT '',
			endpoint TEXT NOT NULL DEFAULT '',
			account_id TEXT NOT NULL DEFAULT '',
			zone_id TEXT NOT NULL DEFAULT '',
			secret_id TEXT REFERENCES secrets(id) ON DELETE SET NULL,
			status TEXT NOT NULL CHECK(status IN ('configured', 'failed', 'disabled')),
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE system_endpoint_aliases (
			kind TEXT NOT NULL CHECK(kind IN ('center', 'headscale')),
			endpoint TEXT NOT NULL,
			certificate_secret_id TEXT REFERENCES secrets(id) ON DELETE RESTRICT,
			certificate_not_after TEXT NOT NULL DEFAULT '',
			transition_id TEXT NOT NULL DEFAULT '',
			lifecycle_state TEXT NOT NULL DEFAULT 'active' CHECK(lifecycle_state IN ('active', 'retiring', 'failed')),
			retire_after TEXT NOT NULL DEFAULT '',
			dns_account_id TEXT NOT NULL DEFAULT '',
			dns_zone_id TEXT NOT NULL DEFAULT '',
			dns_record_id TEXT NOT NULL DEFAULT '',
			dns_record_type TEXT NOT NULL DEFAULT '',
			dns_record_content TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(kind, endpoint)
		)`,
		`CREATE TABLE headscale_api_keys (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			key_id INTEGER NOT NULL DEFAULT 0,
			key_prefix TEXT NOT NULL DEFAULT '',
			expires_at TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL CHECK(state IN ('ready', 'preparing', 'committing')),
			previous_prefix TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE application_secrets (
			application_id TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE CASCADE,
			secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE cloudflare_tunnels (
			agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
			tunnel_id TEXT NOT NULL UNIQUE,
			tunnel_name TEXT NOT NULL,
			token_secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
			desired_revision INTEGER NOT NULL DEFAULT 0,
			applied_revision INTEGER NOT NULL DEFAULT 0,
			desired_json BLOB NOT NULL DEFAULT '{}',
			status TEXT NOT NULL CHECK(status IN ('pending', 'applying', 'ready', 'failed', 'stopped')),
			attempt INTEGER NOT NULL DEFAULT 0,
			lease_expires_at TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE cloudflare_tunnel_operations (
			agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE RESTRICT,
			account_id TEXT NOT NULL,
			operation_id TEXT NOT NULL UNIQUE,
			tunnel_name TEXT NOT NULL UNIQUE,
			tunnel_secret_id TEXT NOT NULL REFERENCES secrets(id) ON DELETE RESTRICT,
			tunnel_id TEXT NOT NULL DEFAULT '',
			phase TEXT NOT NULL CHECK(phase IN ('intent', 'creating', 'created')),
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE center_remote_access (
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
		)`,
		`CREATE TABLE deployments (
			id TEXT PRIMARY KEY,
			agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			app_key TEXT NOT NULL,
			app_version TEXT NOT NULL,
			manifest_json BLOB NOT NULL,
			config_json BLOB NOT NULL,
			service_address TEXT NOT NULL DEFAULT '',
			secret_id TEXT REFERENCES secrets(id),
			registry_credential_id TEXT REFERENCES registry_credentials(id) ON DELETE RESTRICT,
			operation TEXT NOT NULL CHECK(operation IN ('install', 'upgrade', 'configure', 'uninstall')),
			delete_data INTEGER NOT NULL DEFAULT 0,
			state TEXT NOT NULL CHECK(state IN ('pending', 'running', 'succeeded', 'failed')),
			reconciliation_required INTEGER NOT NULL DEFAULT 0 CHECK(reconciliation_required IN (0, 1)),
			reconciliation_requested INTEGER NOT NULL DEFAULT 0 CHECK(reconciliation_requested IN (0, 1)),
			runtime_generation INTEGER NOT NULL DEFAULT 0,
			executed_runtime_generation INTEGER CHECK(executed_runtime_generation >= 0),
			attempt INTEGER NOT NULL DEFAULT 0,
			lease_expires_at TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE
		)`,
		`CREATE UNIQUE INDEX deployments_one_active_task_idx ON deployments(agent_id, app_key) WHERE state IN ('pending', 'running') OR reconciliation_required = 1`,
		`CREATE TABLE application_commands (
			id TEXT PRIMARY KEY,
			application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
			site_id TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '' COLLATE NOCASE,
			agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			gateway_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
			kind TEXT NOT NULL CHECK(kind IN ('3xui.reality.create', '3xui.reality.verify', '3xui.reality.harden', '3xui.reality.rename', '3xui.subscription.configure', '3xui.clients.manage', '3xui.node.reconcile', '3xui.controller.manage')),
			input_json BLOB NOT NULL,
			result_json BLOB NOT NULL DEFAULT '{}',
			result_secret_id TEXT REFERENCES secrets(id) ON DELETE SET NULL,
			state TEXT NOT NULL CHECK(state IN ('pending', 'running', 'succeeded', 'failed')),
			reconciliation_required INTEGER NOT NULL DEFAULT 0 CHECK(reconciliation_required IN (0, 1)),
			attempt INTEGER NOT NULL DEFAULT 0,
			lease_expires_at TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TRIGGER secret_deliveries_delete_with_deployment AFTER DELETE ON deployments
			BEGIN DELETE FROM secret_deliveries WHERE kind = 'deployment_credentials' AND resource_id = OLD.id; END`,
		`CREATE TRIGGER secret_deliveries_delete_with_application_command AFTER DELETE ON application_commands
			BEGIN DELETE FROM secret_deliveries WHERE kind = 'application_command_result' AND resource_id = OLD.id; END`,
		`CREATE UNIQUE INDEX application_commands_one_active_idx ON application_commands(agent_id) WHERE (state IN ('pending', 'running') OR reconciliation_required = 1) AND kind <> '3xui.controller.manage'`,
		`CREATE UNIQUE INDEX application_commands_one_active_controller_idx ON application_commands(application_id) WHERE (state IN ('pending', 'running') OR reconciliation_required = 1) AND kind = '3xui.controller.manage'`,
		`CREATE UNIQUE INDEX application_commands_one_active_reality_name_idx ON application_commands(site_id, display_name COLLATE NOCASE)
			WHERE site_id <> '' AND display_name <> '' AND kind IN ('3xui.reality.create', '3xui.reality.rename')
			AND (state IN ('pending', 'running') OR reconciliation_required = 1)`,
		`CREATE TRIGGER application_commands_block_during_three_x_ui_migration
			BEFORE INSERT ON application_commands
			WHEN NEW.kind <> '3xui.controller.manage'
			AND NOT (NEW.kind = '3xui.node.reconcile' AND EXISTS (
				SELECT 1 FROM three_x_ui_migrations
				WHERE id = json_extract(CASE WHEN json_valid(NEW.input_json) THEN NEW.input_json ELSE '{}' END, '$.migrationId') AND state = 'switching'
			))
			AND EXISTS (
				SELECT 1 FROM three_x_ui_migrations migration
				JOIN applications queued ON queued.id = NEW.application_id
				WHERE migration.state IN ('backing_up', 'restoring', 'switching')
				AND migration.site_id = queued.site_id
			)
			BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END`,
		`CREATE TRIGGER application_command_updates_block_during_three_x_ui_migration
			BEFORE UPDATE OF application_id, kind, input_json, state ON application_commands
			WHEN NEW.kind <> '3xui.controller.manage' AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1)
			AND NOT (NEW.kind = '3xui.node.reconcile' AND EXISTS (
				SELECT 1 FROM three_x_ui_migrations
				WHERE id = json_extract(CASE WHEN json_valid(NEW.input_json) THEN NEW.input_json ELSE '{}' END, '$.migrationId') AND state = 'switching'
			))
			AND EXISTS (
				SELECT 1 FROM three_x_ui_migrations migration
				JOIN applications queued ON queued.id = NEW.application_id
				WHERE migration.state IN ('backing_up', 'restoring', 'switching')
				AND migration.site_id = queued.site_id
			)
			BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END`,
		`CREATE TRIGGER deployments_block_during_three_x_ui_migration
			BEFORE INSERT ON deployments
			WHEN NEW.app_key = 'vastora-official/3x-ui' AND EXISTS (
				SELECT 1 FROM three_x_ui_migrations
				WHERE state IN ('backing_up', 'restoring', 'switching')
				AND (source_application_id = NEW.application_id OR target_application_id = NEW.application_id)
			)
			BEGIN SELECT RAISE(ABORT, '3x-ui subscription host migration is in progress'); END`,
		`CREATE TRIGGER deployments_block_during_three_x_ui_data_plane
			BEFORE INSERT ON deployments
			WHEN NEW.app_key = 'vastora-official/3x-ui' AND EXISTS (
				SELECT 1 FROM application_commands command
				JOIN applications command_app ON command_app.id = command.application_id
				JOIN applications deployment_app ON deployment_app.id = NEW.application_id
				WHERE (command.state IN ('pending', 'running') OR command.reconciliation_required = 1)
				AND command.kind <> '3xui.controller.manage'
				AND command_app.site_id = deployment_app.site_id
			)
			BEGIN SELECT RAISE(ABORT, '3x-ui data-plane operation is in progress'); END`,
		`CREATE TRIGGER application_commands_block_during_three_x_ui_deployment
			BEFORE INSERT ON application_commands
			WHEN NEW.kind NOT IN ('3xui.controller.manage', '3xui.node.reconcile')
			AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1)
			AND EXISTS (
				SELECT 1 FROM deployments deployment
				JOIN applications deployment_app ON deployment_app.id = deployment.application_id
				JOIN applications command_app ON command_app.id = NEW.application_id
				WHERE deployment.app_key = 'vastora-official/3x-ui'
				AND (deployment.state IN ('pending', 'running') OR deployment.reconciliation_required = 1)
				AND deployment_app.site_id = command_app.site_id
			)
			BEGIN SELECT RAISE(ABORT, '3x-ui deployment is in progress'); END`,
		`CREATE TRIGGER application_command_updates_block_during_three_x_ui_deployment
			BEFORE UPDATE OF application_id, kind, input_json, state, reconciliation_required ON application_commands
			WHEN NEW.kind NOT IN ('3xui.controller.manage', '3xui.node.reconcile')
			AND (NEW.state IN ('pending', 'running') OR NEW.reconciliation_required = 1) AND EXISTS (
				SELECT 1 FROM deployments deployment
				JOIN applications deployment_app ON deployment_app.id = deployment.application_id
				JOIN applications command_app ON command_app.id = NEW.application_id
				WHERE deployment.app_key = 'vastora-official/3x-ui'
				AND (deployment.state IN ('pending', 'running') OR deployment.reconciliation_required = 1)
				AND deployment_app.site_id = command_app.site_id
			)
			BEGIN SELECT RAISE(ABORT, '3x-ui deployment is in progress'); END`,
		`CREATE TABLE task_events (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			kind TEXT NOT NULL,
			revision INTEGER NOT NULL DEFAULT 0,
			event TEXT NOT NULL CHECK(event IN ('queued', 'claimed', 'lease_expired', 'succeeded', 'failed')),
			message TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX task_events_task_idx ON task_events(task_id, created_at)`,
		`CREATE INDEX task_events_agent_idx ON task_events(agent_id, created_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("center: initialize schema: %w", err)
		}
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO organizations(id, name, created_at, updated_at) VALUES(?, 'Vastora', ?, ?)`, defaultOrganizationID, now, now); err != nil {
		return fmt.Errorf("center: create default organization: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, centerSchemaVersion)); err != nil {
		return fmt.Errorf("center: set schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("center: commit schema initialization: %w", err)
	}
	return nil
}
