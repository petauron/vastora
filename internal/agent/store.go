// Package agent persists the last successfully applied startup configuration on
// a host. It deliberately has no access to Center secrets after a job has been
// applied and never uploads runtime application data.
package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/secret"
	_ "modernc.org/sqlite"
)

var errApplicationNotInstalled = errors.New("agent: application is not installed")

type Store struct {
	db      *sql.DB
	key     []byte
	dataDir string
	now     func() time.Time
}

type AppliedInstallation struct {
	InstanceID      string              `json:"instanceId"`
	AppKey          string              `json:"appKey"`
	Version         string              `json:"version"`
	Config          json.RawMessage     `json:"config"`
	Secrets         json.RawMessage     `json:"-"`
	ServiceAddress  string              `json:"serviceAddress"`
	ConfigHash      string              `json:"configHash"`
	AppliedAt       time.Time           `json:"appliedAt"`
	Manifest        catalog.AppManifest `json:"-"`
	ApplicationRole string              `json:"-"`
}

type sealedApplicationState struct {
	Config          json.RawMessage     `json:"config"`
	Secrets         json.RawMessage     `json:"secrets"`
	ServiceAddress  string              `json:"serviceAddress"`
	Manifest        catalog.AppManifest `json:"manifest"`
	ApplicationRole string              `json:"applicationRole"`
}

type InstallationStatus struct {
	InstanceID string    `json:"instanceId"`
	AppKey     string    `json:"appKey"`
	Version    string    `json:"version"`
	ConfigHash string    `json:"configHash"`
	AppliedAt  time.Time `json:"appliedAt"`
}

type Connection struct {
	AgentID       string `json:"agentId"`
	Name          string `json:"name"`
	CenterURL     string `json:"centerUrl"`
	Credential    string `json:"-"`
	PrivateKey    []byte `json:"-"`
	CAFingerprint string `json:"-"`
}

const agentSchemaVersion = 8

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("agent: create data directory: %w", err)
	}
	key, err := secret.LoadOrCreateKey(filepath.Join(dataDir, "agent.key"))
	if err != nil {
		return nil, fmt.Errorf("agent: load local key: %w", err)
	}
	databasePath := filepath.Join(dataDir, "agent.db")
	databaseInfo, statErr := os.Stat(databasePath)
	existingDatabase := statErr == nil && databaseInfo.Size() > 0
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("agent: inspect database: %w", statErr)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("agent: open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, key: key, dataDir: dataDir, now: time.Now}
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
		if version != agentSchemaVersion {
			_ = db.Close()
			return nil, fmt.Errorf("agent: database schema version %d cannot be upgraded by this release", version)
		}
		return store, nil
	}
	if _, err := db.Exec(`CREATE TABLE applied_installations (
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
			ca_fingerprint TEXT NOT NULL
		);
		CREATE TABLE task_receipts (
			task_id TEXT PRIMARY KEY,
			task_kind TEXT NOT NULL,
			attempt INTEGER NOT NULL CHECK(attempt > 0),
			task_hash BLOB NOT NULL,
			state TEXT NOT NULL CHECK(state IN ('processing', 'completed', 'acknowledged', 'reconciliation_required')),
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
		PRAGMA user_version = 8;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("agent: initialize schema: %w", err)
	}
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

const localCenterChannelName = "local-center-channel"

func (s *Store) LocalCenterChannel(centerURL string) (bool, error) {
	content, err := os.ReadFile(filepath.Join(s.dataDir, localCenterChannelName))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("agent: read local Center channel marker: %w", err)
	}
	return strings.TrimSpace(string(content)) == strings.TrimSpace(centerURL), nil
}

func (s *Store) SetLocalCenterChannel(centerURL string) error {
	path := filepath.Join(s.dataDir, localCenterChannelName)
	centerURL = strings.TrimSpace(centerURL)
	if centerURL == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("agent: remove local Center channel marker: %w", err)
		}
		return nil
	}
	temporary, err := os.CreateTemp(s.dataDir, ".local-center-channel-*")
	if err != nil {
		return fmt.Errorf("agent: create local Center channel marker: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(centerURL + "\n"); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("agent: publish local Center channel marker: %w", err)
	}
	return nil
}

type GatewayAppliedState struct {
	Desired      gateway.DesiredState  `json:"desired"`
	Certificates []gateway.Certificate `json:"-"`
	ConfigHash   string                `json:"configHash"`
	AppliedAt    time.Time             `json:"appliedAt"`
}

func (s *Store) GatewayState(ctx context.Context) (GatewayAppliedState, error) {
	var encoded, sealedCertificates []byte
	var state GatewayAppliedState
	var appliedAt string
	err := s.db.QueryRowContext(ctx, `SELECT desired_json, sealed_certificates, config_hash, applied_at FROM gateway_applied_state WHERE id = 1`).Scan(&encoded, &sealedCertificates, &state.ConfigHash, &appliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return GatewayAppliedState{}, errors.New("agent: no applied gateway state")
	}
	if err != nil {
		return GatewayAppliedState{}, fmt.Errorf("agent: read gateway state: %w", err)
	}
	if json.Unmarshal(encoded, &state.Desired) != nil || state.Desired.Validate() != nil {
		return GatewayAppliedState{}, errors.New("agent: stored gateway state is invalid")
	}
	certificateJSON, err := secret.Open(s.key, sealedCertificates, []byte("agent-gateway-certificates"))
	if err != nil || json.Unmarshal(certificateJSON, &state.Certificates) != nil || gateway.ValidateCertificates(state.Certificates) != nil {
		return GatewayAppliedState{}, errors.New("agent: stored gateway certificates are invalid")
	}
	state.AppliedAt, err = time.Parse(time.RFC3339Nano, appliedAt)
	if err != nil {
		return GatewayAppliedState{}, errors.New("agent: stored gateway timestamp is invalid")
	}
	return state, nil
}

func (s *Store) RecordGatewayState(ctx context.Context, desired gateway.DesiredState, certificates []gateway.Certificate) (GatewayAppliedState, error) {
	if err := desired.Validate(); err != nil {
		return GatewayAppliedState{}, err
	}
	desired = desired.Sorted()
	if err := gateway.ValidateCertificates(certificates); err != nil {
		return GatewayAppliedState{}, err
	}
	encoded, err := json.Marshal(desired)
	if err != nil {
		return GatewayAppliedState{}, err
	}
	certificateJSON, err := json.Marshal(certificates)
	if err != nil {
		return GatewayAppliedState{}, err
	}
	sealedCertificates, err := secret.Seal(s.key, certificateJSON, []byte("agent-gateway-certificates"))
	if err != nil {
		return GatewayAppliedState{}, fmt.Errorf("agent: encrypt gateway certificates: %w", err)
	}
	digestInput := append(append([]byte(nil), encoded...), certificateJSON...)
	digest := sha256.Sum256(digestInput)
	now := s.now().UTC()
	state := GatewayAppliedState{Desired: desired, Certificates: append([]gateway.Certificate(nil), certificates...), ConfigHash: hex.EncodeToString(digest[:]), AppliedAt: now}
	_, err = s.db.ExecContext(ctx, `INSERT INTO gateway_applied_state(id, applied_revision, desired_json, sealed_certificates, config_hash, applied_at)
		VALUES(1, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET applied_revision = excluded.applied_revision, desired_json = excluded.desired_json, sealed_certificates = excluded.sealed_certificates, config_hash = excluded.config_hash, applied_at = excluded.applied_at
		WHERE excluded.applied_revision >= gateway_applied_state.applied_revision`, desired.Revision, encoded, sealedCertificates, state.ConfigHash, now.Format(time.RFC3339Nano))
	if err != nil {
		return GatewayAppliedState{}, fmt.Errorf("agent: record gateway state: %w", err)
	}
	return state, nil
}

func (s *Store) ClearGatewayState(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM gateway_applied_state WHERE id = 1`); err != nil {
		return fmt.Errorf("agent: clear gateway state: %w", err)
	}
	return nil
}

func (s *Store) SaveConnection(ctx context.Context, connection Connection) error {
	sealedCredential, sealedPrivateKey, err := s.sealConnection(connection)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO control_plane_connection(id, agent_id, name, center_url, sealed_credential, sealed_private_key, ca_fingerprint) VALUES(1, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`, connection.AgentID, connection.Name, connection.CenterURL, sealedCredential, sealedPrivateKey, normalizeCAFingerprint(connection.CAFingerprint))
	if err != nil {
		return fmt.Errorf("agent: save Center connection: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("agent: save Center connection: %w", err)
	}
	if changed != 1 {
		return errors.New("agent: already enrolled; clear the Agent data directory before enrolling again")
	}
	return nil
}

// ReplaceConnection changes only the control-plane identity. Application,
// gateway, and recovery state remain intact so an explicitly approved Center
// migration cannot stop or forget locally managed workloads.
func (s *Store) ReplaceConnection(ctx context.Context, connection Connection) error {
	sealedCredential, sealedPrivateKey, err := s.sealConnection(connection)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO control_plane_connection(id, agent_id, name, center_url, sealed_credential, sealed_private_key, ca_fingerprint)
		VALUES(1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET agent_id = excluded.agent_id, name = excluded.name, center_url = excluded.center_url, sealed_credential = excluded.sealed_credential, sealed_private_key = excluded.sealed_private_key, ca_fingerprint = excluded.ca_fingerprint`, connection.AgentID, connection.Name, connection.CenterURL, sealedCredential, sealedPrivateKey, normalizeCAFingerprint(connection.CAFingerprint)); err != nil {
		return fmt.Errorf("agent: replace Center connection: %w", err)
	}
	return nil
}

func (s *Store) sealConnection(connection Connection) ([]byte, []byte, error) {
	connection.AgentID = strings.TrimSpace(connection.AgentID)
	connection.Name = strings.TrimSpace(connection.Name)
	connection.CenterURL = strings.TrimSpace(connection.CenterURL)
	if connection.AgentID == "" || connection.Name == "" || connection.CenterURL == "" || connection.Credential == "" {
		return nil, nil, errors.New("agent: incomplete Center connection")
	}
	if _, err := controlplane.PublicKey(connection.PrivateKey); err != nil {
		return nil, nil, errors.New("agent: incomplete Center encryption identity")
	}
	if err := validateCAFingerprint(connection.CenterURL, connection.CAFingerprint); err != nil {
		return nil, nil, err
	}
	sealedCredential, err := secret.Seal(s.key, []byte(connection.Credential), []byte("agent-control-plane:"+connection.AgentID))
	if err != nil {
		return nil, nil, fmt.Errorf("agent: encrypt Center credential: %w", err)
	}
	sealedPrivateKey, err := secret.Seal(s.key, connection.PrivateKey, []byte("agent-control-plane-key:"+connection.AgentID))
	if err != nil {
		return nil, nil, fmt.Errorf("agent: encrypt Center identity: %w", err)
	}
	return sealedCredential, sealedPrivateKey, nil
}

func (s *Store) Connection(ctx context.Context) (Connection, error) {
	var connection Connection
	var sealedCredential, sealedPrivateKey []byte
	err := s.db.QueryRowContext(ctx, `SELECT agent_id, name, center_url, sealed_credential, sealed_private_key, ca_fingerprint FROM control_plane_connection WHERE id = 1`).Scan(&connection.AgentID, &connection.Name, &connection.CenterURL, &sealedCredential, &sealedPrivateKey, &connection.CAFingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return Connection{}, errors.New("agent: not enrolled")
	}
	if err != nil {
		return Connection{}, fmt.Errorf("agent: read Center connection: %w", err)
	}
	credential, err := secret.Open(s.key, sealedCredential, []byte("agent-control-plane:"+connection.AgentID))
	if err != nil {
		return Connection{}, fmt.Errorf("agent: decrypt Center credential: %w", err)
	}
	connection.Credential = string(credential)
	privateKey, err := secret.Open(s.key, sealedPrivateKey, []byte("agent-control-plane-key:"+connection.AgentID))
	if err != nil {
		return Connection{}, fmt.Errorf("agent: decrypt Center identity: %w", err)
	}
	if _, err := controlplane.PublicKey(privateKey); err != nil {
		return Connection{}, errors.New("agent: Center encryption identity is invalid; enroll the Agent again")
	}
	connection.PrivateKey = privateKey
	if connection.CAFingerprint != "" {
		if err := validateCAFingerprint(connection.CenterURL, connection.CAFingerprint); err != nil {
			return Connection{}, err
		}
	} else if !loopbackCenterURL(connection.CenterURL) {
		// Schema-3 Agents acquire and persist the verified CA root before their
		// first post-upgrade request. New enrollments always have a pin.
		return connection, nil
	}
	return connection, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// RecordApplied stores a configuration only after a deployment executor has
// completed validation, image pull, typed deployment, and health checks. The executor is
// intentionally a separate future component so cache writes cannot claim a
// deployment succeeded by themselves.
func (s *Store) RecordApplied(ctx context.Context, installation AppliedInstallation) (InstallationStatus, error) {
	if strings.TrimSpace(installation.InstanceID) == "" || strings.TrimSpace(installation.AppKey) == "" || strings.TrimSpace(installation.Version) == "" {
		return InstallationStatus{}, errors.New("agent: instance id, app key, and version are required")
	}
	if !strings.Contains(installation.AppKey, "/") {
		return InstallationStatus{}, errors.New("agent: app key must be source-id/app-id")
	}
	canonicalConfig, err := canonicalJSONObject(installation.Config)
	if err != nil {
		return InstallationStatus{}, fmt.Errorf("agent: configuration: %w", err)
	}
	canonicalSecrets, err := canonicalJSONObject(installation.Secrets)
	if err != nil {
		return InstallationStatus{}, fmt.Errorf("agent: secrets: %w", err)
	}
	if installation.Manifest.ID != "" {
		if err := catalog.ValidateApp(installation.Manifest); err != nil || !strings.HasSuffix(installation.AppKey, "/"+installation.Manifest.ID) || installation.Version != installation.Manifest.Version {
			return InstallationStatus{}, errors.New("agent: applied application manifest is invalid")
		}
	}
	configHash := sha256.Sum256(canonicalConfig)
	encodedState, err := json.Marshal(sealedApplicationState{
		Config: canonicalConfig, Secrets: canonicalSecrets, ServiceAddress: installation.ServiceAddress,
		Manifest: installation.Manifest, ApplicationRole: installation.ApplicationRole,
	})
	if err != nil {
		return InstallationStatus{}, fmt.Errorf("agent: encode applied application state: %w", err)
	}
	sealedState, err := secret.Seal(s.key, encodedState, applicationStateContext(installation.InstanceID))
	if err != nil {
		return InstallationStatus{}, fmt.Errorf("agent: encrypt applied application state: %w", err)
	}
	now := s.now().UTC()
	status := InstallationStatus{InstanceID: installation.InstanceID, AppKey: installation.AppKey, Version: installation.Version, ConfigHash: hex.EncodeToString(configHash[:]), AppliedAt: now}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InstallationStatus{}, fmt.Errorf("agent: begin applied state update: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM applied_installations WHERE app_key = ? AND instance_id <> ?`, installation.AppKey, installation.InstanceID); err != nil {
		return InstallationStatus{}, fmt.Errorf("agent: replace previous applied state: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO applied_installations(instance_id, app_key, version, sealed_state, config_hash, applied_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_id) DO UPDATE SET app_key=excluded.app_key, version=excluded.version,
		sealed_state=excluded.sealed_state, config_hash=excluded.config_hash, applied_at=excluded.applied_at`, status.InstanceID, status.AppKey, status.Version, sealedState, status.ConfigHash, status.AppliedAt.Format(time.RFC3339Nano))
	if err != nil {
		return InstallationStatus{}, fmt.Errorf("agent: record applied state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return InstallationStatus{}, fmt.Errorf("agent: commit applied state: %w", err)
	}
	return status, nil
}

func (s *Store) ListApplied(ctx context.Context) ([]InstallationStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT instance_id, app_key, version, config_hash, applied_at FROM applied_installations ORDER BY instance_id`)
	if err != nil {
		return nil, fmt.Errorf("agent: list applied state: %w", err)
	}
	defer rows.Close()
	var statuses []InstallationStatus
	for rows.Next() {
		var status InstallationStatus
		var appliedAt string
		if err := rows.Scan(&status.InstanceID, &status.AppKey, &status.Version, &status.ConfigHash, &appliedAt); err != nil {
			return nil, fmt.Errorf("agent: scan applied state: %w", err)
		}
		status.AppliedAt, err = time.Parse(time.RFC3339Nano, appliedAt)
		if err != nil {
			return nil, fmt.Errorf("agent: parse applied time: %w", err)
		}
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

func (s *Store) AppliedConfig(ctx context.Context, appKey string) (json.RawMessage, error) {
	installation, err := s.AppliedInstallation(ctx, appKey)
	if err != nil {
		return nil, err
	}
	return installation.Config, nil
}

func (s *Store) AppliedInstallation(ctx context.Context, appKey string) (AppliedInstallation, error) {
	var value AppliedInstallation
	var sealedState []byte
	var appliedAt string
	err := s.db.QueryRowContext(ctx, `SELECT instance_id, app_key, version, sealed_state, config_hash, applied_at FROM applied_installations WHERE app_key = ? ORDER BY applied_at DESC LIMIT 1`, appKey).Scan(&value.InstanceID, &value.AppKey, &value.Version, &sealedState, &value.ConfigHash, &appliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AppliedInstallation{}, errApplicationNotInstalled
	}
	if err != nil {
		return AppliedInstallation{}, fmt.Errorf("agent: read applied application: %w", err)
	}
	state, err := s.openApplicationState(value.InstanceID, sealedState)
	if err != nil {
		return AppliedInstallation{}, err
	}
	value.Config = state.Config
	value.Secrets = state.Secrets
	value.ServiceAddress = state.ServiceAddress
	value.Manifest = state.Manifest
	value.ApplicationRole = state.ApplicationRole
	if value.Manifest.ID != "" {
		if catalog.ValidateApp(value.Manifest) != nil || !strings.HasSuffix(value.AppKey, "/"+value.Manifest.ID) || value.Version != value.Manifest.Version {
			return AppliedInstallation{}, errors.New("agent: persisted application manifest is invalid")
		}
	}
	value.AppliedAt, err = time.Parse(time.RFC3339Nano, appliedAt)
	if err != nil {
		return AppliedInstallation{}, fmt.Errorf("agent: parse applied application time: %w", err)
	}
	return value, nil
}

func (s *Store) openApplicationState(instanceID string, sealedState []byte) (sealedApplicationState, error) {
	plain, err := secret.Open(s.key, sealedState, applicationStateContext(instanceID))
	if err != nil {
		return sealedApplicationState{}, fmt.Errorf("agent: decrypt applied application state: %w", err)
	}
	var state sealedApplicationState
	if json.Unmarshal(plain, &state) != nil {
		return sealedApplicationState{}, errors.New("agent: applied application state is invalid")
	}
	if _, err := canonicalJSONObject(state.Config); err != nil {
		return sealedApplicationState{}, errors.New("agent: applied application configuration is invalid")
	}
	if _, err := canonicalJSONObject(state.Secrets); err != nil {
		return sealedApplicationState{}, errors.New("agent: applied application secrets are invalid")
	}
	return state, nil
}

// RestorableInstallations returns the encrypted last-known-good application
// states in deterministic order. Secret bytes are decrypted only into the
// returned task material and never written to logs or Docker configuration.
func (s *Store) RestorableInstallations(ctx context.Context) ([]AppliedInstallation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT app_key FROM applied_installations ORDER BY app_key`)
	if err != nil {
		return nil, fmt.Errorf("agent: list restorable applications: %w", err)
	}
	defer rows.Close()
	var appKeys []string
	for rows.Next() {
		var appKey string
		if err := rows.Scan(&appKey); err != nil {
			return nil, fmt.Errorf("agent: scan restorable application: %w", err)
		}
		appKeys = append(appKeys, appKey)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agent: list restorable applications: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("agent: close restorable application list: %w", err)
	}
	installations := make([]AppliedInstallation, 0, len(appKeys))
	for _, appKey := range appKeys {
		installation, err := s.AppliedInstallation(ctx, appKey)
		if err != nil {
			return nil, err
		}
		installations = append(installations, installation)
	}
	return installations, nil
}

func (s *Store) RemoveApplied(ctx context.Context, appKey string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM applied_installations WHERE app_key = ?`, appKey); err != nil {
		return fmt.Errorf("agent: remove applied state: %w", err)
	}
	return nil
}

func (s *Store) ReadAppliedSecrets(ctx context.Context, instanceID string) (json.RawMessage, error) {
	var sealedState []byte
	if err := s.db.QueryRowContext(ctx, `SELECT sealed_state FROM applied_installations WHERE instance_id = ?`, instanceID).Scan(&sealedState); err != nil {
		return nil, fmt.Errorf("agent: read applied secrets: %w", err)
	}
	state, err := s.openApplicationState(instanceID, sealedState)
	if err != nil {
		return nil, err
	}
	return state.Secrets, nil
}

func canonicalJSONObject(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("must be a JSON object")
	}
	var value map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, errors.New("must be a JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("must contain one JSON value")
	}
	return json.Marshal(value)
}
