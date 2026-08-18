// Package center owns desired configuration, catalog metadata, and web
// authentication. It has no Docker dependency by design.
package center

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/secret"
	"golang.org/x/crypto/argon2"
	_ "modernc.org/sqlite"
)

const sessionLifetime = 24 * time.Hour

type Store struct {
	db      *sql.DB
	dataDir string
	key     []byte
	now     func() time.Time
}

type SourceInput struct {
	ID             string
	DisplayName    string
	URL            string
	PublicKey      []byte
	BearerToken    string
	CustomCAPEM    string
	RefreshSeconds int
}

type CatalogSource struct {
	ID             string    `json:"id"`
	DisplayName    string    `json:"displayName"`
	URL            string    `json:"url"`
	PublicKey      string    `json:"publicKey"`
	CustomCASet    bool      `json:"customCASet"`
	BearerTokenSet bool      `json:"bearerTokenSet"`
	Enabled        bool      `json:"enabled"`
	RefreshSeconds int       `json:"refreshIntervalSeconds"`
	FetchedAt      time.Time `json:"fetchedAt,omitempty"`
	LastError      string    `json:"lastError,omitempty"`
}

type sourceCredential struct {
	CatalogSource
	publicKey   []byte
	bearerToken string
	customCA    []byte
	etag        string
	lastMod     string
}

type AppView struct {
	Key       string              `json:"key"`
	SourceID  string              `json:"sourceId"`
	App       catalog.AppManifest `json:"app"`
	FetchedAt time.Time           `json:"fetchedAt"`
}

type RegistryCredential struct {
	ID        string    `json:"id"`
	Host      string    `json:"host"`
	Username  string    `json:"username"`
	TokenSet  bool      `json:"tokenSet"`
	CreatedAt time.Time `json:"createdAt"`
}

type AgentEnrollment struct {
	Token              string    `json:"token"`
	SiteID             string    `json:"siteId"`
	ExpiresAt          time.Time `json:"expiresAt"`
	HeadscaleCommand   string    `json:"headscaleCommand,omitempty"`
	HeadscaleExpiresAt time.Time `json:"headscaleExpiresAt,omitempty"`
}

type AgentCredential struct {
	ID         string `json:"id"`
	Credential string `json:"credential"`
}

type AgentView struct {
	ID                   string                 `json:"id"`
	Name                 string                 `json:"name"`
	Version              string                 `json:"version"`
	Status               string                 `json:"status"`
	AppliedInstallations int                    `json:"appliedInstallations"`
	EnrolledAt           time.Time              `json:"enrolledAt"`
	LastSeenAt           time.Time              `json:"lastSeenAt"`
	Connected            bool                   `json:"connected"`
	SiteID               string                 `json:"siteId"`
	Roles                []string               `json:"roles"`
	Capabilities         NodeCapabilities       `json:"capabilities"`
	NetworkCandidates    []networking.Candidate `json:"networkCandidates"`
	NetworkProfile       *networking.Profile    `json:"networkProfile,omitempty"`
	GatewayHealthy       bool                   `json:"gatewayHealthy"`
}

const OfficialCatalogSourceID = "vastora-official"

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("center: create data directory: %w", err)
	}
	key, err := secret.LoadOrCreateKey(filepath.Join(dataDir, "center.key"))
	if err != nil {
		return nil, fmt.Errorf("center: load root key: %w", err)
	}
	databasePath := filepath.Join(dataDir, "center.db")
	databaseInfo, statErr := os.Stat(databasePath)
	existingDatabase := statErr == nil && databaseInfo.Size() > 0
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("center: inspect database: %w", statErr)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("center: open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, dataDir: dataDir, key: key, now: time.Now}
	if err := store.initializeSchema(context.Background(), existingDatabase); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.ensureHeadscaleDNSFile(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) IsConfigured(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&count); err != nil {
		return false, fmt.Errorf("center: read administrators: %w", err)
	}
	return count > 0, nil
}

func (s *Store) CreateFirstAdmin(ctx context.Context, username, password string) (string, string, error) {
	username = strings.TrimSpace(username)
	if !usernamePattern.MatchString(username) {
		return "", "", errors.New("center: username must be 3 to 64 characters using letters, numbers, dots, underscores, or hyphens")
	}
	if len(password) < 12 {
		return "", "", errors.New("center: password must be at least 12 characters")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", fmt.Errorf("center: begin setup: %w", err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&count); err != nil {
		return "", "", fmt.Errorf("center: read administrators: %w", err)
	}
	if count != 0 {
		return "", "", errors.New("center: setup is already complete")
	}
	passwordHash, err := hashPassword(password)
	if err != nil {
		return "", "", err
	}
	adminID, err := randomToken(18)
	if err != nil {
		return "", "", err
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO admins(id, username, password_hash, created_at) VALUES(?, ?, ?, ?)`, adminID, username, passwordHash, now.Format(time.RFC3339Nano)); err != nil {
		return "", "", fmt.Errorf("center: create administrator: %w", err)
	}
	sessionToken, csrfToken, err := createSession(ctx, tx, adminID, now)
	if err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", fmt.Errorf("center: commit setup: %w", err)
	}
	return sessionToken, csrfToken, nil
}

func (s *Store) Authenticate(ctx context.Context, username, password string) (string, string, error) {
	username = strings.TrimSpace(username)
	var adminID, passwordHash string
	err := s.db.QueryRowContext(ctx, `SELECT id, password_hash FROM admins WHERE username = ?`, username).Scan(&adminID, &passwordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", errors.New("center: invalid credentials")
	}
	if err != nil {
		return "", "", fmt.Errorf("center: read administrator: %w", err)
	}
	if !verifyPassword(password, passwordHash) {
		return "", "", errors.New("center: invalid credentials")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", fmt.Errorf("center: begin session: %w", err)
	}
	defer tx.Rollback()
	token, csrf, err := createSession(ctx, tx, adminID, s.now().UTC())
	if err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", fmt.Errorf("center: commit session: %w", err)
	}
	return token, csrf, nil
}

func (s *Store) ValidateSession(ctx context.Context, sessionToken, csrfToken string, mutation bool) error {
	if sessionToken == "" {
		return errors.New("center: authentication required")
	}
	var storedCSRF, expiresAt string
	err := s.db.QueryRowContext(ctx, `SELECT csrf_token, expires_at FROM sessions WHERE token_hash = ?`, tokenHash(sessionToken)).Scan(&storedCSRF, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: session is invalid")
	}
	if err != nil {
		return fmt.Errorf("center: read session: %w", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !expires.After(s.now()) {
		return errors.New("center: session has expired")
	}
	if mutation && subtle.ConstantTimeCompare([]byte(storedCSRF), []byte(csrfToken)) != 1 {
		return errors.New("center: CSRF token is invalid")
	}
	return nil
}

func (s *Store) ChangePassword(ctx context.Context, sessionToken, currentPassword, newPassword string) error {
	if sessionToken == "" {
		return errors.New("center: authentication required")
	}
	if len(newPassword) < 12 {
		return errors.New("center: new password must be at least 12 characters")
	}
	if currentPassword == newPassword {
		return errors.New("center: new password must be different")
	}
	var adminID, passwordHash, expiresAt string
	sessionHash := tokenHash(sessionToken)
	err := s.db.QueryRowContext(ctx, `SELECT a.id, a.password_hash, s.expires_at FROM sessions s JOIN admins a ON a.id = s.admin_id WHERE s.token_hash = ?`, sessionHash).Scan(&adminID, &passwordHash, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: session is invalid")
	}
	if err != nil {
		return fmt.Errorf("center: read administrator session: %w", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !expires.After(s.now()) {
		return errors.New("center: session has expired")
	}
	if !verifyPassword(currentPassword, passwordHash) {
		return errors.New("center: current password is incorrect")
	}
	nextHash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("center: begin password change: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE admins SET password_hash = ? WHERE id = ? AND password_hash = ?`, nextHash, adminID, passwordHash)
	if err != nil {
		return fmt.Errorf("center: update administrator password: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("center: administrator password changed concurrently; retry")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE admin_id = ? AND token_hash <> ?`, adminID, sessionHash); err != nil {
		return fmt.Errorf("center: revoke other sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("center: commit password change: %w", err)
	}
	return nil
}

func (s *Store) CreateSource(ctx context.Context, input SourceInput) error {
	if !sourceIDPattern.MatchString(input.ID) {
		return errors.New("center: source id must use lowercase letters, digits, and hyphens")
	}
	if strings.TrimSpace(input.DisplayName) == "" || strings.TrimSpace(input.URL) == "" {
		return errors.New("center: source display name and URL are required")
	}
	if len(input.PublicKey) != 32 {
		return errors.New("center: source requires an Ed25519 public key")
	}
	if input.RefreshSeconds == 0 {
		input.RefreshSeconds = 21600
	}
	if input.RefreshSeconds < 300 || input.RefreshSeconds > 604800 {
		return errors.New("center: refresh interval must be between 300 and 604800 seconds")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("center: begin source: %w", err)
	}
	defer tx.Rollback()
	var bearerSecretID any
	if input.BearerToken != "" {
		id, err := s.putSecret(ctx, tx, []byte(input.BearerToken), "catalog-source:"+input.ID)
		if err != nil {
			return err
		}
		bearerSecretID = id
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO catalog_sources(
		id, display_name, url, public_key, bearer_secret_id, custom_ca, enabled, refresh_seconds, created_at
	) VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`, input.ID, input.DisplayName, input.URL, input.PublicKey, bearerSecretID, input.CustomCAPEM, input.RefreshSeconds, s.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("center: create source: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("center: commit source: %w", err)
	}
	return nil
}

func (s *Store) ListSources(ctx context.Context) ([]CatalogSource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id, s.display_name, s.url, s.public_key, s.bearer_secret_id,
		s.custom_ca, s.enabled, s.refresh_seconds, COALESCE(c.fetched_at, ''), s.last_error
		FROM catalog_sources s LEFT JOIN catalog_cache c ON c.source_id = s.id ORDER BY s.id`)
	if err != nil {
		return nil, fmt.Errorf("center: list sources: %w", err)
	}
	defer rows.Close()
	// API consumers distinguish an empty collection from null. Returning an
	// allocated slice keeps the catalog contract stable for a new Center.
	sources := make([]CatalogSource, 0)
	for rows.Next() {
		var source CatalogSource
		var key, bearerID, customCA []byte
		var enabled int
		var fetchedAt string
		if err := rows.Scan(&source.ID, &source.DisplayName, &source.URL, &key, &bearerID, &customCA, &enabled, &source.RefreshSeconds, &fetchedAt, &source.LastError); err != nil {
			return nil, fmt.Errorf("center: scan source: %w", err)
		}
		source.PublicKey = base64.RawURLEncoding.EncodeToString(key)
		source.BearerTokenSet = len(bearerID) > 0
		source.CustomCASet = len(customCA) > 0
		source.Enabled = enabled == 1
		if fetchedAt != "" {
			source.FetchedAt, _ = time.Parse(time.RFC3339Nano, fetchedAt)
		}
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

func (s *Store) SourceForRefresh(ctx context.Context, id string) (sourceCredential, error) {
	var source sourceCredential
	var bearerSecretID sql.NullString
	var cachedSourceID sql.NullString
	var customCA []byte
	err := s.db.QueryRowContext(ctx, `SELECT s.id, s.display_name, s.url, s.public_key, s.bearer_secret_id, s.custom_ca,
		s.enabled, s.refresh_seconds, COALESCE(c.fetched_at, ''), s.last_error,
		COALESCE(c.etag, ''), COALESCE(c.last_modified, ''), c.source_id
		FROM catalog_sources s LEFT JOIN catalog_cache c ON c.source_id = s.id WHERE s.id = ?`, id).Scan(
		&source.ID, &source.DisplayName, &source.URL, &source.publicKey, &bearerSecretID, &customCA,
		&source.Enabled, &source.RefreshSeconds, new(string), &source.LastError, &source.etag, &source.lastMod, &cachedSourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return sourceCredential{}, errors.New("center: catalog source not found")
	}
	if err != nil {
		return sourceCredential{}, fmt.Errorf("center: read source: %w", err)
	}
	if !source.Enabled {
		return sourceCredential{}, errors.New("center: catalog source is disabled")
	}
	source.customCA = customCA
	if bearerSecretID.Valid {
		value, err := s.getSecret(ctx, bearerSecretID.String, "catalog-source:"+source.ID)
		if err != nil {
			return sourceCredential{}, err
		}
		source.bearerToken = string(value)
	}
	return source, nil
}

func (s *Store) SaveCatalog(ctx context.Context, sourceID string, rawEnvelope []byte, etag, lastModified string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO catalog_cache(source_id, envelope, etag, last_modified, fetched_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(source_id) DO UPDATE SET envelope=excluded.envelope, etag=excluded.etag,
		last_modified=excluded.last_modified, fetched_at=excluded.fetched_at`, sourceID, rawEnvelope, etag, lastModified, s.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("center: save catalog: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE catalog_sources SET last_error = '' WHERE id = ?`, sourceID)
	return err
}

func (s *Store) RecordCatalogError(ctx context.Context, sourceID string, refreshErr error) error {
	message := "catalog refresh failed"
	if refreshErr != nil {
		message = refreshErr.Error()
	}
	if len(message) > 512 {
		message = message[:512]
	}
	_, err := s.db.ExecContext(ctx, `UPDATE catalog_sources SET last_error = ? WHERE id = ?`, message, sourceID)
	return err
}

func (s *Store) ClearCatalogError(ctx context.Context, sourceID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE catalog_sources SET last_error = '' WHERE id = ?`, sourceID)
	return err
}

func (s *Store) ListApps(ctx context.Context) ([]AppView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id, s.public_key, c.envelope, c.fetched_at
		FROM catalog_sources s JOIN catalog_cache c ON c.source_id=s.id WHERE s.enabled=1 ORDER BY s.id`)
	if err != nil {
		return nil, fmt.Errorf("center: list apps: %w", err)
	}
	defer rows.Close()
	// Keep JSON responses as [] when no verified catalog has been fetched yet.
	apps := make([]AppView, 0)
	for rows.Next() {
		var sourceID, fetchedAt string
		var publicKey, rawEnvelope []byte
		if err := rows.Scan(&sourceID, &publicKey, &rawEnvelope, &fetchedAt); err != nil {
			return nil, fmt.Errorf("center: scan catalog cache: %w", err)
		}
		envelope, err := catalog.ParseEnvelope(rawEnvelope)
		if err != nil {
			return nil, fmt.Errorf("center: cached envelope: %w", err)
		}
		parsedCatalog, _, err := catalog.Verify(envelope, publicKey)
		if err != nil {
			return nil, fmt.Errorf("center: cached catalog: %w", err)
		}
		at, err := time.Parse(time.RFC3339Nano, fetchedAt)
		if err != nil {
			return nil, fmt.Errorf("center: cached catalog timestamp: %w", err)
		}
		for _, app := range parsedCatalog.Apps {
			apps = append(apps, AppView{Key: sourceID + "/" + app.ID, SourceID: sourceID, App: app, FetchedAt: at})
		}
	}
	return apps, rows.Err()
}

func (s *Store) CreateRegistryCredential(ctx context.Context, host, username, token string) (RegistryCredential, error) {
	if strings.TrimSpace(host) == "" || strings.TrimSpace(username) == "" || token == "" {
		return RegistryCredential{}, errors.New("center: registry host, username, and token are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RegistryCredential{}, fmt.Errorf("center: begin registry credential: %w", err)
	}
	defer tx.Rollback()
	id, err := randomToken(18)
	if err != nil {
		return RegistryCredential{}, err
	}
	secretID, err := s.putSecret(ctx, tx, []byte(token), "registry-credential:"+id)
	if err != nil {
		return RegistryCredential{}, err
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO registry_credentials(id, host, username, secret_id, created_at) VALUES(?, ?, ?, ?, ?)`, id, host, username, secretID, now.Format(time.RFC3339Nano)); err != nil {
		return RegistryCredential{}, fmt.Errorf("center: create registry credential: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RegistryCredential{}, fmt.Errorf("center: commit registry credential: %w", err)
	}
	return RegistryCredential{ID: id, Host: host, Username: username, TokenSet: true, CreatedAt: now}, nil
}

func (s *Store) CreateAgentEnrollment(ctx context.Context, siteID string) (AgentEnrollment, error) {
	siteID = strings.TrimSpace(siteID)
	if siteID == "" {
		siteID = defaultSiteID
	}
	var siteExists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites WHERE id = ? AND status = 'active'`, siteID).Scan(&siteExists); err != nil {
		return AgentEnrollment{}, fmt.Errorf("center: inspect enrollment site: %w", err)
	}
	if siteExists != 1 {
		return AgentEnrollment{}, errors.New("center: enrollment site was not found")
	}
	token, err := randomToken(32)
	if err != nil {
		return AgentEnrollment{}, err
	}
	enrollment := AgentEnrollment{Token: token, SiteID: siteID, ExpiresAt: s.now().UTC().Add(10 * time.Minute)}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO agent_enrollment_tokens(token_hash, site_id, expires_at) VALUES(?, ?, ?)`, tokenHash(token), siteID, enrollment.ExpiresAt.Format(time.RFC3339Nano)); err != nil {
		return AgentEnrollment{}, fmt.Errorf("center: create agent enrollment: %w", err)
	}
	return enrollment, nil
}

func (s *Store) SeedOfficialCatalog(ctx context.Context, payload []byte) error {
	if _, err := catalog.ParseCatalog(payload); err != nil {
		return fmt.Errorf("center: official catalog: %w", err)
	}
	var secretID string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'official_catalog_signing_key'`).Scan(&secretID)
	if errors.Is(err, sql.ErrNoRows) {
		publicKey, privateKey, keyErr := ed25519.GenerateKey(rand.Reader)
		if keyErr != nil {
			return fmt.Errorf("center: generate official catalog key: %w", keyErr)
		}
		tx, txErr := s.db.BeginTx(ctx, nil)
		if txErr != nil {
			return fmt.Errorf("center: begin official catalog key: %w", txErr)
		}
		defer tx.Rollback()
		secretID, keyErr = s.putSecret(ctx, tx, privateKey, "official-catalog-signing-key")
		if keyErr != nil {
			return keyErr
		}
		if _, keyErr = tx.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES('official_catalog_signing_key', ?)`, secretID); keyErr != nil {
			return fmt.Errorf("center: save official catalog key: %w", keyErr)
		}
		if keyErr = tx.Commit(); keyErr != nil {
			return fmt.Errorf("center: commit official catalog key: %w", keyErr)
		}
		return s.saveOfficialCatalog(ctx, payload, publicKey, privateKey)
	}
	if err != nil {
		return fmt.Errorf("center: read official catalog key: %w", err)
	}
	privateKey, err := s.getSecret(ctx, secretID, "official-catalog-signing-key")
	if err != nil {
		return err
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("center: official catalog signing key is invalid")
	}
	return s.saveOfficialCatalog(ctx, payload, ed25519.PrivateKey(privateKey).Public().(ed25519.PublicKey), ed25519.PrivateKey(privateKey))
}

func (s *Store) saveOfficialCatalog(ctx context.Context, payload, publicKey []byte, privateKey ed25519.PrivateKey) error {
	envelope, err := catalog.Sign("vastora-official", privateKey, payload)
	if err != nil {
		return fmt.Errorf("center: sign official catalog: %w", err)
	}
	rawEnvelope, err := catalog.MarshalEnvelope(envelope)
	if err != nil {
		return fmt.Errorf("center: marshal official catalog: %w", err)
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO catalog_sources(id, display_name, url, public_key, enabled, refresh_seconds, created_at)
		VALUES(?, 'Vastora Official', 'builtin://vastora-official', ?, 1, 86400, ?)
		ON CONFLICT(id) DO UPDATE SET public_key=excluded.public_key, enabled=1`, OfficialCatalogSourceID, publicKey, now); err != nil {
		return fmt.Errorf("center: save official catalog source: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO catalog_cache(source_id, envelope, fetched_at) VALUES(?, ?, ?)
		ON CONFLICT(source_id) DO UPDATE SET envelope=excluded.envelope, fetched_at=excluded.fetched_at`, OfficialCatalogSourceID, rawEnvelope, now); err != nil {
		return fmt.Errorf("center: cache official catalog: %w", err)
	}
	return nil
}

func (s *Store) OfficialCatalogEnvelope(ctx context.Context) ([]byte, error) {
	var envelope []byte
	if err := s.db.QueryRowContext(ctx, `SELECT envelope FROM catalog_cache WHERE source_id = ?`, OfficialCatalogSourceID).Scan(&envelope); err != nil {
		return nil, fmt.Errorf("center: read official catalog: %w", err)
	}
	return envelope, nil
}

func (s *Store) EnrollAgent(ctx context.Context, enrollmentToken, name, version string) (AgentCredential, error) {
	name = strings.TrimSpace(name)
	version = strings.TrimSpace(version)
	if name == "" || len(name) > 128 {
		return AgentCredential{}, errors.New("center: agent name must be 1 to 128 characters")
	}
	if version == "" || len(version) > 128 {
		return AgentCredential{}, errors.New("center: agent version is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentCredential{}, fmt.Errorf("center: begin agent enrollment: %w", err)
	}
	defer tx.Rollback()
	var expiresAt, siteID string
	var usedAt sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT expires_at, used_at, site_id FROM agent_enrollment_tokens WHERE token_hash = ?`, tokenHash(enrollmentToken)).Scan(&expiresAt, &usedAt, &siteID)
	if errors.Is(err, sql.ErrNoRows) || usedAt.Valid {
		return AgentCredential{}, errors.New("center: agent enrollment token is invalid")
	}
	if err != nil {
		return AgentCredential{}, fmt.Errorf("center: read agent enrollment: %w", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !expires.After(s.now()) {
		return AgentCredential{}, errors.New("center: agent enrollment token has expired")
	}
	id, err := randomToken(18)
	if err != nil {
		return AgentCredential{}, err
	}
	credential, err := randomToken(32)
	if err != nil {
		return AgentCredential{}, err
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents(id, name, credential_hash, version, status, enrolled_at, last_seen_at, site_id, roles_json, capabilities_json) VALUES(?, ?, ?, ?, 'active', ?, ?, ?, '[]', '{}')`, id, name, tokenHash(credential), version, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), siteID); err != nil {
		return AgentCredential{}, fmt.Errorf("center: save agent: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_enrollment_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL`, now.Format(time.RFC3339Nano), tokenHash(enrollmentToken))
	if err != nil {
		return AgentCredential{}, fmt.Errorf("center: consume agent enrollment: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return AgentCredential{}, errors.New("center: agent enrollment token is invalid")
	}
	if err := tx.Commit(); err != nil {
		return AgentCredential{}, fmt.Errorf("center: commit agent enrollment: %w", err)
	}
	return AgentCredential{ID: id, Credential: credential}, nil
}

func (s *Store) RecordAgentHeartbeat(ctx context.Context, id, credential string, heartbeat NodeHeartbeat) error {
	if heartbeat.AppliedInstallations < 0 || heartbeat.AppliedInstallations > 1_000_000 {
		return errors.New("center: invalid applied installation count")
	}
	if err := s.authenticateAgent(ctx, id, credential); err != nil {
		return err
	}
	heartbeat.Roles = uniqueStrings(heartbeat.Roles)
	for _, role := range heartbeat.Roles {
		if role != "worker" && role != "gateway" {
			return errors.New("center: Agent reported an unsupported role")
		}
	}
	if heartbeat.Capabilities.Gateway && !containsString(heartbeat.Roles, "gateway") {
		return errors.New("center: gateway capability requires gateway role")
	}
	if heartbeat.Capabilities.Docker && !containsString(heartbeat.Roles, "worker") {
		return errors.New("center: Docker capability requires worker role")
	}
	if heartbeat.Capabilities.Tunnel && !containsString(heartbeat.Roles, "worker") {
		return errors.New("center: tunnel capability requires worker role")
	}
	if len(heartbeat.NetworkCandidates) > 128 {
		return errors.New("center: Agent reported too many network addresses")
	}
	if len(heartbeat.ApplicationEndpoints) > 512 {
		return errors.New("center: Agent reported too many application endpoints")
	}
	seenAddresses := map[string]bool{}
	for _, candidate := range heartbeat.NetworkCandidates {
		ip := net.ParseIP(candidate.Address)
		if ip == nil || candidate.Interface == "" || (candidate.Family != "ipv4" && candidate.Family != "ipv6") || (candidate.Kind != networking.KindLAN && candidate.Kind != networking.KindHeadscale && candidate.Kind != networking.KindPublic) {
			return errors.New("center: Agent reported an invalid network candidate")
		}
		if seenAddresses[ip.String()] {
			return errors.New("center: Agent reported a duplicate network candidate")
		}
		seenAddresses[ip.String()] = true
	}
	rolesJSON, _ := json.Marshal(heartbeat.Roles)
	capabilitiesJSON, _ := json.Marshal(heartbeat.Capabilities)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET version = ?, applied_installations = ?, roles_json = ?, capabilities_json = ?, gateway_healthy = ?, last_seen_at = ? WHERE id = ?`, strings.TrimSpace(heartbeat.Version), heartbeat.AppliedInstallations, rolesJSON, capabilitiesJSON, heartbeat.GatewayHealthy, now.Format(time.RFC3339Nano), id); err != nil {
		return fmt.Errorf("center: record agent heartbeat: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_network_candidates WHERE agent_id = ?`, id); err != nil {
		return fmt.Errorf("center: replace Agent network candidates: %w", err)
	}
	for _, candidate := range heartbeat.NetworkCandidates {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_network_candidates(agent_id, address, interface_name, family, kind, observed_at) VALUES(?, ?, ?, ?, ?, ?)`, id, net.ParseIP(candidate.Address).String(), candidate.Interface, candidate.Family, candidate.Kind, now.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("center: record Agent network candidate: %w", err)
		}
	}
	if err := s.reconcileApplicationEndpoints(ctx, tx, id, heartbeat.ApplicationEndpoints, now); err != nil {
		return err
	}
	if heartbeat.Capabilities.Gateway && !heartbeat.GatewayHealthy {
		result, err := tx.ExecContext(ctx, `UPDATE gateway_components SET generation = generation + 1, status = 'failed', lease_expires_at = '', last_error = 'gateway health check failed; queued for reconcile', updated_at = ? WHERE gateway_node_id = ? AND desired_status = 'running' AND status = 'ready'`, now.Format(time.RFC3339Nano), id)
		if err != nil {
			return fmt.Errorf("center: queue unhealthy gateway reconcile: %w", err)
		}
		changed, _ := result.RowsAffected()
		if changed != 0 {
			var generation int64
			if err := tx.QueryRowContext(ctx, `SELECT generation FROM gateway_components WHERE gateway_node_id = ?`, id).Scan(&generation); err != nil {
				return err
			}
			if err := s.recordTaskEvent(ctx, tx, gatewayComponentTaskID(id, generation), id, "gateway.component.apply", generation, "queued", "gateway health check failed; queued for reconcile"); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) ListAgents(ctx context.Context) ([]AgentView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, version, status, applied_installations, enrolled_at, last_seen_at, site_id, roles_json, capabilities_json, gateway_healthy FROM agents ORDER BY status, name, id`)
	if err != nil {
		return nil, fmt.Errorf("center: list agents: %w", err)
	}
	agents := make([]AgentView, 0)
	for rows.Next() {
		var agent AgentView
		var enrolledAt, lastSeenAt string
		var rolesJSON, capabilitiesJSON []byte
		var gatewayHealthy int
		if err := rows.Scan(&agent.ID, &agent.Name, &agent.Version, &agent.Status, &agent.AppliedInstallations, &enrolledAt, &lastSeenAt, &agent.SiteID, &rolesJSON, &capabilitiesJSON, &gatewayHealthy); err != nil {
			return nil, fmt.Errorf("center: scan agent: %w", err)
		}
		var err error
		agent.EnrolledAt, err = time.Parse(time.RFC3339Nano, enrolledAt)
		if err != nil {
			return nil, fmt.Errorf("center: parse agent enrollment time: %w", err)
		}
		agent.LastSeenAt, err = time.Parse(time.RFC3339Nano, lastSeenAt)
		if err != nil {
			return nil, fmt.Errorf("center: parse agent heartbeat time: %w", err)
		}
		agent.Connected = agent.Status == "active" && agent.LastSeenAt.After(s.now().Add(-45*time.Second))
		agent.GatewayHealthy = gatewayHealthy == 1
		if json.Unmarshal(rolesJSON, &agent.Roles) != nil || json.Unmarshal(capabilitiesJSON, &agent.Capabilities) != nil {
			return nil, errors.New("center: invalid stored Agent capabilities")
		}
		if agent.Roles == nil {
			agent.Roles = []string{}
		}
		agents = append(agents, agent)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range agents {
		candidates, err := s.networkCandidates(ctx, agents[index].ID)
		if err != nil {
			return nil, err
		}
		agents[index].NetworkCandidates = candidates
		profile, err := s.networkProfile(ctx, agents[index].ID)
		if err != nil {
			return nil, err
		}
		agents[index].NetworkProfile = profile
	}
	return agents, rows.Err()
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (s *Store) HasActiveDeployment(ctx context.Context, agentID, appKey string) (bool, error) {
	var operation string
	err := s.db.QueryRowContext(ctx, `SELECT operation FROM deployments
		WHERE agent_id = ? AND app_key = ? AND state = 'succeeded'
		ORDER BY created_at DESC, rowid DESC LIMIT 1`, agentID, appKey).Scan(&operation)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("center: read active deployment: %w", err)
	}
	return operation == "install" || operation == "upgrade", nil
}

func (s *Store) putSecret(ctx context.Context, tx *sql.Tx, value []byte, additionalData string) (string, error) {
	id, err := randomToken(18)
	if err != nil {
		return "", err
	}
	sealed, err := secret.Seal(s.key, value, []byte(additionalData))
	if err != nil {
		return "", err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO secrets(id, sealed, created_at, updated_at) VALUES(?, ?, ?, ?)`, id, sealed, now, now); err != nil {
		return "", fmt.Errorf("center: save secret: %w", err)
	}
	return id, nil
}

func (s *Store) getSecret(ctx context.Context, id, additionalData string) ([]byte, error) {
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE id = ?`, id).Scan(&sealed); err != nil {
		return nil, fmt.Errorf("center: read secret: %w", err)
	}
	value, err := secret.Open(s.key, sealed, []byte(additionalData))
	if err != nil {
		return nil, fmt.Errorf("center: decrypt secret: %w", err)
	}
	return value, nil
}

func createSession(ctx context.Context, tx *sql.Tx, adminID string, now time.Time) (string, string, error) {
	sessionToken, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	csrfToken, err := randomToken(24)
	if err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions(token_hash, csrf_token, admin_id, expires_at) VALUES(?, ?, ?, ?)`, tokenHash(sessionToken), csrfToken, adminID, now.Add(sessionLifetime).Format(time.RFC3339Nano)); err != nil {
		return "", "", fmt.Errorf("center: create session: %w", err)
	}
	return sessionToken, csrfToken, nil
}

func tokenHash(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func randomToken(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("center: generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("center: generate password salt: %w", err)
	}
	encoded := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return "argon2id$v=19$m=65536,t=3,p=2$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(encoded), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" || parts[1] != "v=19" || parts[2] != "m=65536,t=3,p=2" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(expected) != 32 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return subtle.ConstantTimeCompare(expected, actual) == 1
}

var sourceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,63}$`)
