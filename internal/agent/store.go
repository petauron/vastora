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

	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/secret"
	_ "modernc.org/sqlite"
)

var errApplicationNotInstalled = errors.New("agent: application is not installed")

type Store struct {
	db  *sql.DB
	key []byte
	now func() time.Time
}

type AppliedInstallation struct {
	InstanceID     string          `json:"instanceId"`
	AppKey         string          `json:"appKey"`
	Version        string          `json:"version"`
	Config         json.RawMessage `json:"config"`
	Secrets        json.RawMessage `json:"-"`
	ServiceAddress string          `json:"serviceAddress"`
	ConfigHash     string          `json:"configHash"`
	AppliedAt      time.Time       `json:"appliedAt"`
}

type InstallationStatus struct {
	InstanceID string    `json:"instanceId"`
	AppKey     string    `json:"appKey"`
	Version    string    `json:"version"`
	ConfigHash string    `json:"configHash"`
	AppliedAt  time.Time `json:"appliedAt"`
}

type Connection struct {
	AgentID    string `json:"agentId"`
	Name       string `json:"name"`
	CenterURL  string `json:"centerUrl"`
	Credential string `json:"-"`
}

const agentSchemaVersion = 3

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
	store := &Store{db: db, key: key, now: time.Now}
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
			config_json BLOB NOT NULL,
			sealed_secrets BLOB NOT NULL,
			service_address TEXT NOT NULL,
			config_hash TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
		CREATE TABLE control_plane_connection (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			agent_id TEXT NOT NULL,
			name TEXT NOT NULL,
			center_url TEXT NOT NULL,
			sealed_credential BLOB NOT NULL
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
		PRAGMA user_version = 3;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("agent: initialize schema: %w", err)
	}
	return store, nil
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
	sealed, err := s.sealConnection(connection)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO control_plane_connection(id, agent_id, name, center_url, sealed_credential) VALUES(1, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`, connection.AgentID, connection.Name, connection.CenterURL, sealed)
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
	sealed, err := s.sealConnection(connection)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO control_plane_connection(id, agent_id, name, center_url, sealed_credential)
		VALUES(1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET agent_id = excluded.agent_id, name = excluded.name, center_url = excluded.center_url, sealed_credential = excluded.sealed_credential`, connection.AgentID, connection.Name, connection.CenterURL, sealed); err != nil {
		return fmt.Errorf("agent: replace Center connection: %w", err)
	}
	return nil
}

func (s *Store) sealConnection(connection Connection) ([]byte, error) {
	connection.AgentID = strings.TrimSpace(connection.AgentID)
	connection.Name = strings.TrimSpace(connection.Name)
	connection.CenterURL = strings.TrimSpace(connection.CenterURL)
	if connection.AgentID == "" || connection.Name == "" || connection.CenterURL == "" || connection.Credential == "" {
		return nil, errors.New("agent: incomplete Center connection")
	}
	sealed, err := secret.Seal(s.key, []byte(connection.Credential), []byte("agent-control-plane:"+connection.AgentID))
	if err != nil {
		return nil, fmt.Errorf("agent: encrypt Center credential: %w", err)
	}
	return sealed, nil
}

func (s *Store) Connection(ctx context.Context) (Connection, error) {
	var connection Connection
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT agent_id, name, center_url, sealed_credential FROM control_plane_connection WHERE id = 1`).Scan(&connection.AgentID, &connection.Name, &connection.CenterURL, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return Connection{}, errors.New("agent: not enrolled")
	}
	if err != nil {
		return Connection{}, fmt.Errorf("agent: read Center connection: %w", err)
	}
	credential, err := secret.Open(s.key, sealed, []byte("agent-control-plane:"+connection.AgentID))
	if err != nil {
		return Connection{}, fmt.Errorf("agent: decrypt Center credential: %w", err)
	}
	connection.Credential = string(credential)
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
	configHash := sha256.Sum256(canonicalConfig)
	sealed, err := secret.Seal(s.key, canonicalSecrets, []byte("agent-instance:"+installation.InstanceID))
	if err != nil {
		return InstallationStatus{}, fmt.Errorf("agent: encrypt secrets: %w", err)
	}
	now := s.now().UTC()
	status := InstallationStatus{InstanceID: installation.InstanceID, AppKey: installation.AppKey, Version: installation.Version, ConfigHash: hex.EncodeToString(configHash[:]), AppliedAt: now}
	_, err = s.db.ExecContext(ctx, `INSERT INTO applied_installations(instance_id, app_key, version, config_json, sealed_secrets, service_address, config_hash, applied_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_id) DO UPDATE SET app_key=excluded.app_key, version=excluded.version,
		config_json=excluded.config_json, sealed_secrets=excluded.sealed_secrets, service_address=excluded.service_address, config_hash=excluded.config_hash,
		applied_at=excluded.applied_at`, status.InstanceID, status.AppKey, status.Version, canonicalConfig, sealed, installation.ServiceAddress, status.ConfigHash, status.AppliedAt.Format(time.RFC3339Nano))
	if err != nil {
		return InstallationStatus{}, fmt.Errorf("agent: record applied state: %w", err)
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
	var config []byte
	err := s.db.QueryRowContext(ctx, `SELECT config_json FROM applied_installations WHERE app_key = ? ORDER BY applied_at DESC LIMIT 1`, appKey).Scan(&config)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errApplicationNotInstalled
	}
	if err != nil {
		return nil, fmt.Errorf("agent: read applied application: %w", err)
	}
	return json.RawMessage(config), nil
}

func (s *Store) AppliedInstallation(ctx context.Context, appKey string) (AppliedInstallation, error) {
	var value AppliedInstallation
	var sealed []byte
	var appliedAt string
	err := s.db.QueryRowContext(ctx, `SELECT instance_id, app_key, version, config_json, sealed_secrets, service_address, config_hash, applied_at FROM applied_installations WHERE app_key = ? ORDER BY applied_at DESC LIMIT 1`, appKey).Scan(&value.InstanceID, &value.AppKey, &value.Version, &value.Config, &sealed, &value.ServiceAddress, &value.ConfigHash, &appliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AppliedInstallation{}, errApplicationNotInstalled
	}
	if err != nil {
		return AppliedInstallation{}, fmt.Errorf("agent: read applied application: %w", err)
	}
	plain, err := secret.Open(s.key, sealed, []byte("agent-instance:"+value.InstanceID))
	if err != nil {
		return AppliedInstallation{}, fmt.Errorf("agent: decrypt applied application: %w", err)
	}
	value.Secrets = plain
	value.AppliedAt, _ = time.Parse(time.RFC3339Nano, appliedAt)
	return value, nil
}

func (s *Store) RemoveApplied(ctx context.Context, appKey string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM applied_installations WHERE app_key = ?`, appKey); err != nil {
		return fmt.Errorf("agent: remove applied state: %w", err)
	}
	return nil
}

func (s *Store) ReadAppliedSecrets(ctx context.Context, instanceID string) (json.RawMessage, error) {
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT sealed_secrets FROM applied_installations WHERE instance_id = ?`, instanceID).Scan(&sealed); err != nil {
		return nil, fmt.Errorf("agent: read applied secrets: %w", err)
	}
	plain, err := secret.Open(s.key, sealed, []byte("agent-instance:"+instanceID))
	if err != nil {
		return nil, fmt.Errorf("agent: decrypt applied secrets: %w", err)
	}
	return plain, nil
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
