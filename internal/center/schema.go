package center

import (
	"context"
	"fmt"
	"time"
)

const centerSchemaVersion = 13

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
			created_at TEXT NOT NULL,
			last_error TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE catalog_cache (
			source_id TEXT PRIMARY KEY REFERENCES catalog_sources(id) ON DELETE CASCADE,
			envelope BLOB NOT NULL,
			etag TEXT NOT NULL DEFAULT '',
			last_modified TEXT NOT NULL DEFAULT '',
			fetched_at TEXT NOT NULL
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
			expires_at TEXT NOT NULL,
			used_at TEXT
		)`,
		`CREATE TABLE agents (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			credential_hash BLOB NOT NULL UNIQUE,
			version TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('active', 'disabled')),
			applied_installations INTEGER NOT NULL DEFAULT 0,
			enrolled_at TEXT NOT NULL,
			last_seen_at TEXT NOT NULL,
			site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE RESTRICT,
			roles_json BLOB NOT NULL DEFAULT '[]',
			capabilities_json BLOB NOT NULL DEFAULT '{}',
			gateway_healthy INTEGER NOT NULL DEFAULT 0
		)`,
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
			family TEXT NOT NULL CHECK(family IN ('ipv4', 'ipv6')),
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
		`CREATE TABLE applications (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
			site_id TEXT NOT NULL REFERENCES sites(id) ON DELETE RESTRICT,
			app_key TEXT NOT NULL,
			image TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL CHECK(status IN ('pending', 'deploying', 'running', 'failed', 'stopped')),
			runtime TEXT NOT NULL DEFAULT 'docker',
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
		`CREATE TABLE publications (
			id TEXT PRIMARY KEY,
			service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
			kind TEXT NOT NULL CHECK(kind IN ('lan_gateway', 'headscale_gateway', 'public_direct', 'public_shared_443', 'cloudflare_tunnel')),
			gateway_node_id TEXT REFERENCES agents(id) ON DELETE RESTRICT,
			hostname TEXT NOT NULL,
			sni_hostname TEXT NOT NULL DEFAULT '',
			dns_provider TEXT NOT NULL CHECK(dns_provider IN ('manual', 'cloudflare', 'headscale')),
			dns_record_id TEXT NOT NULL DEFAULT '',
			certificate_secret_id TEXT REFERENCES secrets(id) ON DELETE SET NULL,
			certificate_not_after TEXT NOT NULL DEFAULT '',
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
		`CREATE TABLE deployments (
			id TEXT PRIMARY KEY,
			agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			app_key TEXT NOT NULL,
			app_version TEXT NOT NULL,
			manifest_json BLOB NOT NULL,
			config_json BLOB NOT NULL,
			secret_id TEXT REFERENCES secrets(id),
			operation TEXT NOT NULL CHECK(operation IN ('install', 'upgrade', 'configure', 'uninstall')),
			delete_data INTEGER NOT NULL DEFAULT 0,
			state TEXT NOT NULL CHECK(state IN ('pending', 'running', 'succeeded', 'failed')),
			attempt INTEGER NOT NULL DEFAULT 0,
			lease_expires_at TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE
		)`,
		`CREATE UNIQUE INDEX deployments_one_active_task_idx ON deployments(agent_id, app_key) WHERE state IN ('pending', 'running')`,
		`CREATE TABLE application_commands (
			id TEXT PRIMARY KEY,
			application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
			agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
			gateway_node_id TEXT NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
			kind TEXT NOT NULL CHECK(kind IN ('3xui.reality.create', '3xui.reality.rename', '3xui.subscription.configure', '3xui.clients.manage', '3xui.node.reconcile', '3xui.controller.manage')),
			input_json BLOB NOT NULL,
			result_json BLOB NOT NULL DEFAULT '{}',
			result_secret_id TEXT REFERENCES secrets(id) ON DELETE SET NULL,
			state TEXT NOT NULL CHECK(state IN ('pending', 'running', 'succeeded', 'failed')),
			attempt INTEGER NOT NULL DEFAULT 0,
			lease_expires_at TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE UNIQUE INDEX application_commands_one_active_idx ON application_commands(application_id) WHERE state IN ('pending', 'running') AND kind <> '3xui.controller.manage'`,
		`CREATE UNIQUE INDEX application_commands_one_active_controller_idx ON application_commands(application_id) WHERE state IN ('pending', 'running') AND kind = '3xui.controller.manage'`,
		`CREATE TRIGGER application_commands_block_during_three_x_ui_migration
			BEFORE INSERT ON application_commands
			WHEN NEW.kind <> '3xui.controller.manage' AND EXISTS (
				SELECT 1 FROM three_x_ui_migrations
				WHERE state IN ('backing_up', 'restoring', 'switching')
				AND (source_application_id = NEW.application_id OR target_application_id = NEW.application_id)
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
