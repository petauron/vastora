package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/secret"
	_ "modernc.org/sqlite"
)

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("agent: create data directory: %w", err)
	}
	configDir, err := resolveGlivelaConfigDir(dataDir)
	if err != nil {
		return nil, err
	}
	databasePath := filepath.Join(dataDir, "agent.db")
	databaseInfo, statErr := os.Stat(databasePath)
	existingDatabase := statErr == nil && databaseInfo.Size() > 0
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("agent: inspect database: %w", statErr)
	}
	keyPath := filepath.Join(dataDir, "agent.key")
	_, keyStatErr := os.Stat(keyPath)
	keyExists := keyStatErr == nil
	if keyStatErr != nil && !errors.Is(keyStatErr, os.ErrNotExist) {
		return nil, fmt.Errorf("agent: inspect local key: %w", keyStatErr)
	}
	if existingDatabase && !keyExists {
		return nil, errors.New("agent: existing database requires its original local key")
	}
	if !existingDatabase && keyExists {
		return nil, errors.New("agent: local key exists without its database")
	}
	var key []byte
	if existingDatabase {
		key, err = secret.LoadKey(keyPath)
	} else {
		key, err = secret.CreateKey(keyPath)
	}
	if err != nil {
		return nil, fmt.Errorf("agent: load local key: %w", err)
	}
	freshComplete := existingDatabase
	if !existingDatabase {
		defer func() {
			if freshComplete {
				return
			}
			_ = os.Remove(keyPath)
			_ = os.Remove(databasePath)
			_ = os.Remove(databasePath + "-wal")
			_ = os.Remove(databasePath + "-shm")
		}()
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("agent: open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, key: key, dataDir: dataDir, glivelaConfigDir: configDir, now: time.Now, linkChecker: landing.NewLinkChecker()}
	var existingVersion int
	if existingDatabase {
		if err := db.QueryRow(`PRAGMA user_version`).Scan(&existingVersion); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("agent: inspect schema version: %w", err)
		}
		if existingVersion < minSupportedAgentSchemaVersion {
			_ = db.Close()
			return nil, fmt.Errorf("agent: database schema version %d predates supported baseline %d; upgrade through Vastora v0.1.0 first", existingVersion, minSupportedAgentSchemaVersion)
		}
		if existingVersion > agentSchemaVersion {
			_ = db.Close()
			return nil, fmt.Errorf("agent: database schema version %d cannot be upgraded by this release", existingVersion)
		}
		if err := inspectAgentDatabaseKeyBinding(context.Background(), db, key); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if _, err := db.Exec(`PRAGMA journal_mode = WAL; PRAGMA foreign_keys = ON;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("agent: initialize database: %w", err)
	}
	if existingDatabase {
		if existingVersion == minSupportedAgentSchemaVersion {
			if err := store.promoteBaselineSchema(context.Background()); err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 25 to 26: %w", err)
			}
		}
		return store, nil
	}
	if _, err := db.Exec(`CREATE TABLE storage_key_binding (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			sealed BLOB NOT NULL
		);
		CREATE TABLE applied_installations (
			instance_id TEXT PRIMARY KEY,
			app_key TEXT NOT NULL,
			version TEXT NOT NULL,
			sealed_state BLOB NOT NULL,
			config_hash TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
		CREATE TABLE control_plane_connection (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			landing_only INTEGER NOT NULL DEFAULT 0 CHECK(landing_only IN (0,1)),
			agent_id TEXT NOT NULL,
			name TEXT NOT NULL,
			center_url TEXT NOT NULL,
			sealed_credential BLOB NOT NULL,
			sealed_private_key BLOB NOT NULL,
			ca_fingerprint TEXT NOT NULL,
			ca_certificate_pem TEXT NOT NULL
		);
		CREATE TABLE agent_install_operations (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			operation_id TEXT NOT NULL UNIQUE,
			center_url TEXT NOT NULL,
			token_hash BLOB NOT NULL,
			sealed_token BLOB NOT NULL,
			sealed_private_key BLOB NOT NULL,
			ca_fingerprint TEXT NOT NULL,
			ca_certificate_pem TEXT NOT NULL,
			replace_existing INTEGER NOT NULL CHECK(replace_existing IN (0, 1)),
			phase TEXT NOT NULL CHECK(phase IN ('enrollment_pending', 'enrolled', 'unit_written', 'reloaded', 'enabled', 'started', 'healthy')),
			agent_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			roles_json BLOB NOT NULL DEFAULT '[]',
			capabilities_json BLOB NOT NULL DEFAULT '{}',
			previous_agent_id TEXT NOT NULL DEFAULT '',
			previous_name TEXT NOT NULL DEFAULT '',
			previous_center_url TEXT NOT NULL DEFAULT '',
			previous_sealed_credential BLOB NOT NULL DEFAULT X'',
			previous_sealed_private_key BLOB NOT NULL DEFAULT X'',
			previous_ca_fingerprint TEXT NOT NULL DEFAULT '',
			previous_ca_certificate_pem TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE three_x_ui_controller_promotions (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			migration_id TEXT NOT NULL UNIQUE,
			task_id TEXT NOT NULL UNIQUE,
			application_id TEXT NOT NULL,
			command_hash BLOB NOT NULL,
			phase TEXT NOT NULL CHECK(phase IN ('prepared', 'imported', 'api_ready', 'role_configured', 'applied')),
			sealed_state BLOB NOT NULL,
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE task_receipts (
			task_id TEXT PRIMARY KEY,
			task_kind TEXT NOT NULL,
			runtime_generation INTEGER NOT NULL CHECK(runtime_generation >= 0),
			attempt INTEGER NOT NULL CHECK(attempt > 0),
			task_hash BLOB NOT NULL,
			state TEXT NOT NULL CHECK(state IN ('processing', 'completed', 'acknowledged', 'reconciliation_required', 'reconciliation_acknowledged')),
			sealed_completion BLOB,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE gateway_applied_state (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			applied_revision INTEGER NOT NULL,
			desired_json BLOB NOT NULL,
			sealed_certificates BLOB NOT NULL,
			config_hash TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
		CREATE TABLE node_listener_applied_state (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			applied_revision INTEGER NOT NULL,
			desired_json BLOB NOT NULL,
			config_hash TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
		CREATE TABLE three_x_ui_reset_journal (
			operation_key TEXT PRIMARY KEY,
			service_id TEXT NOT NULL,
			expected_next_reset_at TEXT NOT NULL,
			plan_revision INTEGER NOT NULL CHECK(plan_revision > 0),
			target_inbound_id INTEGER NOT NULL CHECK(target_inbound_id > 0),
			target_inbound_tag TEXT NOT NULL,
			sync_used_bytes INTEGER NOT NULL CHECK(sync_used_bytes >= 0),
			desired_enabled INTEGER NOT NULL CHECK(desired_enabled IN (0, 1)),
			status TEXT NOT NULL CHECK(status IN ('disable_started', 'disabled', 'reset_applied', 'reset_done', 'enable_done', 'retry', 'retry_applied', 'restore_pending', 'restore_pending_applied', 'completed', 'cancelled', 'cancelled_applied')),
			last_error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE landing_runtime_state (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			sealed_state BLOB NOT NULL
		);
		CREATE TABLE landing_controller_state (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			sealed_state BLOB NOT NULL
		);
		CREATE TABLE glivela_runtime_state (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			sealed_state BLOB NOT NULL
		);
		CREATE TABLE xray_worker_state (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			sealed_state BLOB NOT NULL
		);
		CREATE TABLE node_site_journal (application_id TEXT PRIMARY KEY, sealed_state BLOB NOT NULL);
		CREATE TABLE shared_caddy_sites (application_id TEXT PRIMARY KEY, sealed_state BLOB NOT NULL);
		` + taskReceiptIndexesSQL + `PRAGMA user_version = 26;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("agent: initialize schema: %w", err)
	}
	if err := store.initializeDatabaseKeyBinding(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	freshComplete = true
	return store, nil
}

func applicationStateContext(instanceID string) []byte {
	return []byte("agent-application-state:" + instanceID)
}
