// Package node persists the last successfully applied startup configuration on
// a host. It deliberately has no access to Master secrets after a job has been
// applied and never uploads runtime application data.
package node

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

	"github.com/petauron/vastora/internal/secret"
	_ "modernc.org/sqlite"
)

type Store struct {
	db  *sql.DB
	key []byte
	now func() time.Time
}

type AppliedInstallation struct {
	InstanceID string          `json:"instanceId"`
	AppKey     string          `json:"appKey"`
	Version    string          `json:"version"`
	Config     json.RawMessage `json:"config"`
	Secrets    json.RawMessage `json:"-"`
	ConfigHash string          `json:"configHash"`
	AppliedAt  time.Time       `json:"appliedAt"`
}

type InstallationStatus struct {
	InstanceID string    `json:"instanceId"`
	AppKey     string    `json:"appKey"`
	Version    string    `json:"version"`
	ConfigHash string    `json:"configHash"`
	AppliedAt  time.Time `json:"appliedAt"`
}

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("node: create data directory: %w", err)
	}
	key, err := secret.LoadOrCreateKey(filepath.Join(dataDir, "node.key"))
	if err != nil {
		return nil, fmt.Errorf("node: load local key: %w", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "node.db"))
	if err != nil {
		return nil, fmt.Errorf("node: open database: %w", err)
	}
	store := &Store{db: db, key: key, now: time.Now}
	if _, err := db.Exec(`PRAGMA journal_mode = WAL; PRAGMA foreign_keys = ON;
		CREATE TABLE IF NOT EXISTS applied_installations (
			instance_id TEXT PRIMARY KEY,
			app_key TEXT NOT NULL,
			version TEXT NOT NULL,
			config_json BLOB NOT NULL,
			sealed_secrets BLOB NOT NULL,
			config_hash TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("node: migrate database: %w", err)
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// RecordApplied stores a configuration only after a deployment executor has
// completed validation, pull, compose up, and health checks. The executor is
// intentionally a separate future component so cache writes cannot claim a
// deployment succeeded by themselves.
func (s *Store) RecordApplied(ctx context.Context, installation AppliedInstallation) (InstallationStatus, error) {
	if strings.TrimSpace(installation.InstanceID) == "" || strings.TrimSpace(installation.AppKey) == "" || strings.TrimSpace(installation.Version) == "" {
		return InstallationStatus{}, errors.New("node: instance id, app key, and version are required")
	}
	if !strings.Contains(installation.AppKey, "/") {
		return InstallationStatus{}, errors.New("node: app key must be source-id/app-id")
	}
	canonicalConfig, err := canonicalJSONObject(installation.Config)
	if err != nil {
		return InstallationStatus{}, fmt.Errorf("node: configuration: %w", err)
	}
	canonicalSecrets, err := canonicalJSONObject(installation.Secrets)
	if err != nil {
		return InstallationStatus{}, fmt.Errorf("node: secrets: %w", err)
	}
	configHash := sha256.Sum256(canonicalConfig)
	sealed, err := secret.Seal(s.key, canonicalSecrets, []byte("node-instance:"+installation.InstanceID))
	if err != nil {
		return InstallationStatus{}, fmt.Errorf("node: encrypt secrets: %w", err)
	}
	now := s.now().UTC()
	status := InstallationStatus{InstanceID: installation.InstanceID, AppKey: installation.AppKey, Version: installation.Version, ConfigHash: hex.EncodeToString(configHash[:]), AppliedAt: now}
	_, err = s.db.ExecContext(ctx, `INSERT INTO applied_installations(instance_id, app_key, version, config_json, sealed_secrets, config_hash, applied_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(instance_id) DO UPDATE SET app_key=excluded.app_key, version=excluded.version,
		config_json=excluded.config_json, sealed_secrets=excluded.sealed_secrets, config_hash=excluded.config_hash,
		applied_at=excluded.applied_at`, status.InstanceID, status.AppKey, status.Version, canonicalConfig, sealed, status.ConfigHash, status.AppliedAt.Format(time.RFC3339Nano))
	if err != nil {
		return InstallationStatus{}, fmt.Errorf("node: record applied state: %w", err)
	}
	return status, nil
}

func (s *Store) ListApplied(ctx context.Context) ([]InstallationStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT instance_id, app_key, version, config_hash, applied_at FROM applied_installations ORDER BY instance_id`)
	if err != nil {
		return nil, fmt.Errorf("node: list applied state: %w", err)
	}
	defer rows.Close()
	var statuses []InstallationStatus
	for rows.Next() {
		var status InstallationStatus
		var appliedAt string
		if err := rows.Scan(&status.InstanceID, &status.AppKey, &status.Version, &status.ConfigHash, &appliedAt); err != nil {
			return nil, fmt.Errorf("node: scan applied state: %w", err)
		}
		status.AppliedAt, err = time.Parse(time.RFC3339Nano, appliedAt)
		if err != nil {
			return nil, fmt.Errorf("node: parse applied time: %w", err)
		}
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

func (s *Store) ReadAppliedSecrets(ctx context.Context, instanceID string) (json.RawMessage, error) {
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT sealed_secrets FROM applied_installations WHERE instance_id = ?`, instanceID).Scan(&sealed); err != nil {
		return nil, fmt.Errorf("node: read applied secrets: %w", err)
	}
	plain, err := secret.Open(s.key, sealed, []byte("node-instance:"+instanceID))
	if err != nil {
		return nil, fmt.Errorf("node: decrypt applied secrets: %w", err)
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
