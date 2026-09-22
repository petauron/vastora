package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/secret"
	_ "modernc.org/sqlite"
)

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("agent: create data directory: %w", err)
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
	var err error
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
	store := &Store{db: db, key: key, dataDir: dataDir, now: time.Now, linkChecker: landing.NewLinkChecker()}
	if existingDatabase {
		bound, err := inspectAgentDatabaseKeyBinding(context.Background(), db, key)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		if !bound {
			if err := verifyAgentEncryptedState(context.Background(), db, key); err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: verify legacy encrypted state before binding the database: %w", err)
			}
		}
	}
	if _, err := db.Exec(`PRAGMA journal_mode = WAL; PRAGMA foreign_keys = ON;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("agent: initialize database: %w", err)
	}
	if existingDatabase {
		var version int
		if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("agent: inspect schema version: %w", err)
		}
		if version == 1 {
			emptyCertificates, sealErr := secret.Seal(key, []byte(`[]`), []byte("agent-gateway-certificates"))
			if sealErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: encrypt empty gateway certificates: %w", sealErr)
			}
			tx, migrateErr := db.Begin()
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`ALTER TABLE gateway_applied_state ADD COLUMN sealed_certificates BLOB`)
			}
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`UPDATE gateway_applied_state SET sealed_certificates = ?`, emptyCertificates)
			}
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`PRAGMA user_version = 2`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 1 to 2: %w", migrateErr)
			}
			version = 2
		}
		if version == 2 {
			tx, migrateErr := db.Begin()
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`CREATE TABLE three_x_ui_reset_journal (
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
				)`)
			}
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`PRAGMA user_version = 3`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 2 to 3: %w", migrateErr)
			}
			version = 3
		}
		if version == 3 {
			tx, migrateErr := db.Begin()
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`ALTER TABLE control_plane_connection ADD COLUMN sealed_private_key BLOB NOT NULL DEFAULT X''`)
			}
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`ALTER TABLE control_plane_connection ADD COLUMN ca_fingerprint TEXT NOT NULL DEFAULT ''`)
			}
			if migrateErr == nil {
				var agentID string
				identityErr := tx.QueryRow(`SELECT agent_id FROM control_plane_connection WHERE id = 1`).Scan(&agentID)
				if identityErr == nil {
					privateKey, _, keyErr := controlplane.GenerateKeyPair()
					if keyErr == nil {
						var sealedPrivateKey []byte
						sealedPrivateKey, keyErr = secret.Seal(key, privateKey, []byte("agent-control-plane-key:"+agentID))
						if keyErr == nil {
							_, keyErr = tx.Exec(`UPDATE control_plane_connection SET sealed_private_key = ? WHERE id = 1`, sealedPrivateKey)
						}
					}
					migrateErr = keyErr
				} else if !errors.Is(identityErr, sql.ErrNoRows) {
					migrateErr = identityErr
				}
			}
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`CREATE TABLE task_receipts (
					task_id TEXT PRIMARY KEY,
					attempt INTEGER NOT NULL CHECK(attempt > 0),
					task_hash BLOB NOT NULL,
					state TEXT NOT NULL CHECK(state IN ('processing', 'completed', 'acknowledged')),
					sealed_completion BLOB,
					created_at TEXT NOT NULL,
					updated_at TEXT NOT NULL
				)`)
			}
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`PRAGMA user_version = 4`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 3 to 4: %w", migrateErr)
			}
			version = 4
		}
		if version == 4 {
			tx, migrateErr := db.Begin()
			if migrateErr == nil {
				migrateErr = migrateAppliedInstallationsV5(tx, key)
			}
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`PRAGMA user_version = 5`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 4 to 5: %w", migrateErr)
			}
			version = 5
		}
		if version == 5 {
			tx, migrateErr := db.Begin()
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`CREATE TABLE task_receipts_v6 (
					task_id TEXT PRIMARY KEY,
					attempt INTEGER NOT NULL CHECK(attempt > 0),
					task_hash BLOB NOT NULL,
					state TEXT NOT NULL CHECK(state IN ('processing', 'completed', 'acknowledged', 'reconciliation_required')),
					sealed_completion BLOB,
					created_at TEXT NOT NULL,
					updated_at TEXT NOT NULL
				);
				INSERT INTO task_receipts_v6 SELECT * FROM task_receipts;
				DROP TABLE task_receipts;
				ALTER TABLE task_receipts_v6 RENAME TO task_receipts;
				PRAGMA user_version = 6`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 5 to 6: %w", migrateErr)
			}
			version = 6
		}
		if version == 6 {
			tx, migrateErr := db.Begin()
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`CREATE TABLE task_receipts_v7 (
					task_id TEXT PRIMARY KEY,
					task_kind TEXT NOT NULL,
					attempt INTEGER NOT NULL CHECK(attempt > 0),
					task_hash BLOB NOT NULL,
					state TEXT NOT NULL CHECK(state IN ('processing', 'completed', 'acknowledged', 'reconciliation_required')),
					sealed_completion BLOB,
					created_at TEXT NOT NULL,
					updated_at TEXT NOT NULL
				);
				INSERT INTO task_receipts_v7(task_id, task_kind, attempt, task_hash, state, sealed_completion, created_at, updated_at)
				SELECT task_id, 'legacy', attempt, task_hash, state, sealed_completion, created_at, updated_at FROM task_receipts;
				DROP TABLE task_receipts;
				ALTER TABLE task_receipts_v7 RENAME TO task_receipts;
				PRAGMA user_version = 7`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 6 to 7: %w", migrateErr)
			}
			version = 7
		}
		if version == 7 {
			tx, migrateErr := db.Begin()
			if migrateErr == nil {
				migrateErr = purgeUnrestorableAppliedInstallations(tx, key)
			}
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`PRAGMA user_version = 8`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 7 to 8: %w", migrateErr)
			}
			version = 8
		}
		if version == 8 {
			tx, migrateErr := db.Begin()
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`CREATE TABLE agent_install_operations (
					id INTEGER PRIMARY KEY CHECK(id = 1),
					operation_id TEXT NOT NULL UNIQUE,
					center_url TEXT NOT NULL,
					token_hash BLOB NOT NULL,
					sealed_token BLOB NOT NULL,
					sealed_private_key BLOB NOT NULL,
					ca_fingerprint TEXT NOT NULL,
					replace_existing INTEGER NOT NULL CHECK(replace_existing IN (0, 1)),
					phase TEXT NOT NULL CHECK(phase IN ('enrollment_pending', 'enrolled', 'unit_written', 'reloaded', 'enabled', 'started')),
					agent_id TEXT NOT NULL DEFAULT '',
					name TEXT NOT NULL DEFAULT '',
					roles_json BLOB NOT NULL DEFAULT '[]',
					capabilities_json BLOB NOT NULL DEFAULT '{}',
					last_error TEXT NOT NULL DEFAULT '',
					created_at TEXT NOT NULL,
					updated_at TEXT NOT NULL
				);
				PRAGMA user_version = 9`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 8 to 9: %w", migrateErr)
			}
			version = 9
		}
		if version == 9 {
			tx, migrateErr := db.Begin()
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`CREATE TABLE task_receipts_v10 (
					task_id TEXT PRIMARY KEY,
					task_kind TEXT NOT NULL,
					attempt INTEGER NOT NULL CHECK(attempt > 0),
					task_hash BLOB NOT NULL,
					state TEXT NOT NULL CHECK(state IN ('processing', 'completed', 'acknowledged', 'reconciliation_required', 'reconciliation_acknowledged')),
					sealed_completion BLOB,
					created_at TEXT NOT NULL,
					updated_at TEXT NOT NULL
				);
				INSERT INTO task_receipts_v10 SELECT * FROM task_receipts;
				DROP TABLE task_receipts;
				ALTER TABLE task_receipts_v10 RENAME TO task_receipts;
				PRAGMA user_version = 10`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 9 to 10: %w", migrateErr)
			}
			version = 10
		}
		if version == 10 {
			tx, migrateErr := db.Begin()
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`CREATE TABLE IF NOT EXISTS storage_key_binding (
					id INTEGER PRIMARY KEY CHECK(id = 1),
					sealed BLOB NOT NULL
				);
				PRAGMA user_version = 11`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 10 to 11: %w", migrateErr)
			}
			version = 11
		}
		if version == 11 {
			tx, migrateErr := db.Begin()
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`CREATE TABLE agent_install_operations_v12 (
					id INTEGER PRIMARY KEY CHECK(id = 1),
					operation_id TEXT NOT NULL UNIQUE,
					center_url TEXT NOT NULL,
					token_hash BLOB NOT NULL,
					sealed_token BLOB NOT NULL,
					sealed_private_key BLOB NOT NULL,
					ca_fingerprint TEXT NOT NULL,
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
					last_error TEXT NOT NULL DEFAULT '',
					created_at TEXT NOT NULL,
					updated_at TEXT NOT NULL
				);
				INSERT INTO agent_install_operations_v12(
					id, operation_id, center_url, token_hash, sealed_token, sealed_private_key, ca_fingerprint,
					replace_existing, phase, agent_id, name, roles_json, capabilities_json, last_error, created_at, updated_at
				) SELECT id, operation_id, center_url, token_hash, sealed_token, sealed_private_key, ca_fingerprint,
					replace_existing, phase, agent_id, name, roles_json, capabilities_json, last_error, created_at, updated_at
				FROM agent_install_operations;
				DROP TABLE agent_install_operations;
				ALTER TABLE agent_install_operations_v12 RENAME TO agent_install_operations;
				PRAGMA user_version = 12`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 11 to 12: %w", migrateErr)
			}
			version = 12
		}
		if version == 12 {
			tx, migrateErr := db.Begin()
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`CREATE TABLE IF NOT EXISTS three_x_ui_controller_promotions (
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
			)`)
			}
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`PRAGMA user_version = 13`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 12 to 13: %w", migrateErr)
			}
			version = 13
		}
		if version == 13 {
			tx, migrateErr := db.Begin()
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`ALTER TABLE task_receipts ADD COLUMN runtime_generation INTEGER NOT NULL DEFAULT 0 CHECK(runtime_generation >= 0);
					PRAGMA user_version = 14`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 13 to 14: %w", migrateErr)
			}
			version = 14
		}
		if version == 14 {
			tx, migrateErr := db.Begin()
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`ALTER TABLE control_plane_connection ADD COLUMN ca_certificate_pem TEXT NOT NULL DEFAULT '';
					ALTER TABLE agent_install_operations ADD COLUMN ca_certificate_pem TEXT NOT NULL DEFAULT '';
					ALTER TABLE agent_install_operations ADD COLUMN previous_ca_certificate_pem TEXT NOT NULL DEFAULT '';
					PRAGMA user_version = 15`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 14 to 15: %w", migrateErr)
			}
			version = 15
		}
		if version == 15 {
			tx, migrateErr := db.Begin()
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`CREATE TABLE node_listener_applied_state (
					id INTEGER PRIMARY KEY CHECK(id = 1),
					applied_revision INTEGER NOT NULL,
					desired_json BLOB NOT NULL,
					config_hash TEXT NOT NULL,
					applied_at TEXT NOT NULL
				);
				PRAGMA user_version = 16`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 15 to 16: %w", migrateErr)
			}
			version = 16
		}
		if version == 16 {
			tx, migrateErr := db.Begin()
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`CREATE TABLE landing_runtime_state (
					id INTEGER PRIMARY KEY CHECK(id = 1),
					sealed_state BLOB NOT NULL
				);
				PRAGMA user_version = 17`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 16 to 17: %w", migrateErr)
			}
			version = 17
		}
		if version == 17 {
			// Use a consistent SQLite snapshot, including committed WAL data.
			backupDir, migrateErr := os.MkdirTemp(dataDir, "schema-17-backup-")
			if migrateErr == nil {
				_, migrateErr = db.Exec(`VACUUM INTO ?`, filepath.Join(backupDir, "agent.db"))
			}
			var tx *sql.Tx
			if migrateErr == nil {
				tx, migrateErr = db.Begin()
			}
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`CREATE TABLE landing_controller_state(id INTEGER PRIMARY KEY CHECK(id=1),sealed_state BLOB NOT NULL); PRAGMA user_version=18`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 17 to 18: %w", migrateErr)
			}
			version = 18
		}
		if version == 18 {
			if err := migrateTaskReceiptIndexesV19(db, dataDir); err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 18 to 19: %w", err)
			}
			version = 19
		}
		if version == 19 {
			// The worker state contains REALITY private keys and client UUIDs.
			// Take a consistent database snapshot before adding its encrypted
			// journal so an interrupted forward-only migration is recoverable.
			backupDir, migrateErr := os.MkdirTemp(dataDir, "schema-19-backup-")
			if migrateErr == nil {
				_, migrateErr = db.Exec(`VACUUM INTO ?`, filepath.Join(backupDir, "agent.db"))
			}
			var tx *sql.Tx
			if migrateErr == nil {
				tx, migrateErr = db.Begin()
			}
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`CREATE TABLE IF NOT EXISTS xray_worker_state (
					id INTEGER PRIMARY KEY CHECK(id = 1),
					sealed_state BLOB NOT NULL
				); PRAGMA user_version = 20`)
			}
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`SELECT id, sealed_state FROM xray_worker_state LIMIT 0`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 19 to 20: %w", migrateErr)
			}
			version = 20
		}
		if version == 20 {
			// Preserve the legacy worker journal as one-time cutover input and add
			// a separate complete-artifact journal for Meridian. Translating or
			// renaming the old row during Agent startup would let the still-running
			// legacy writer decode a different state shape before Center has
			// explicitly fenced it.
			backupDir, migrateErr := os.MkdirTemp(dataDir, "schema-20-backup-")
			if migrateErr == nil {
				_, migrateErr = db.Exec(`VACUUM INTO ?`, filepath.Join(backupDir, "agent.db"))
			}
			var tx *sql.Tx
			if migrateErr == nil {
				tx, migrateErr = db.Begin()
			}
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`CREATE TABLE meridian_runtime_state (
					id INTEGER PRIMARY KEY CHECK(id = 1),
					sealed_state BLOB NOT NULL
				)`)
			}
			if migrateErr == nil {
				_, migrateErr = tx.Exec(`PRAGMA user_version=21`)
			}
			if migrateErr == nil {
				migrateErr = tx.Commit()
			} else if tx != nil {
				_ = tx.Rollback()
			}
			if migrateErr != nil {
				_ = db.Close()
				return nil, fmt.Errorf("agent: migrate database schema from 20 to 21: %w", migrateErr)
			}
			version = 21
		}
		if version != agentSchemaVersion {
			_ = db.Close()
			return nil, fmt.Errorf("agent: database schema version %d cannot be upgraded by this release", version)
		}
		if err := store.initializeDatabaseKeyBinding(context.Background()); err != nil {
			_ = db.Close()
			return nil, err
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
		CREATE TABLE meridian_runtime_state (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			sealed_state BLOB NOT NULL
		);
		CREATE TABLE xray_worker_state (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			sealed_state BLOB NOT NULL
		);
		` + taskReceiptIndexesSQL + `PRAGMA user_version = 21;`); err != nil {
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

func migrateAppliedInstallationsV5(tx *sql.Tx, _ []byte) error {
	if _, err := tx.Exec(`CREATE TABLE applied_installations_v5 (
		instance_id TEXT PRIMARY KEY,
		app_key TEXT NOT NULL,
		version TEXT NOT NULL,
		sealed_state BLOB NOT NULL,
		config_hash TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE applied_installations; ALTER TABLE applied_installations_v5 RENAME TO applied_installations`); err != nil {
		return err
	}
	return nil
}

func purgeUnrestorableAppliedInstallations(tx *sql.Tx, key []byte) error {
	rows, err := tx.Query(`SELECT instance_id, sealed_state FROM applied_installations ORDER BY instance_id`)
	if err != nil {
		return err
	}
	var legacyIDs []string
	for rows.Next() {
		var instanceID string
		var sealedState []byte
		if err := rows.Scan(&instanceID, &sealedState); err != nil {
			_ = rows.Close()
			return err
		}
		plain, err := secret.Open(key, sealedState, applicationStateContext(instanceID))
		if err != nil {
			_ = rows.Close()
			return fmt.Errorf("decrypt applied application %s: %w", instanceID, err)
		}
		var state sealedApplicationState
		if json.Unmarshal(plain, &state) != nil {
			_ = rows.Close()
			return fmt.Errorf("decode applied application %s", instanceID)
		}
		if state.Manifest.ID == "" {
			legacyIDs = append(legacyIDs, instanceID)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, instanceID := range legacyIDs {
		if _, err := tx.Exec(`DELETE FROM applied_installations WHERE instance_id = ?`, instanceID); err != nil {
			return err
		}
	}
	return nil
}

func applicationStateContext(instanceID string) []byte {
	return []byte("agent-application-state:" + instanceID)
}
