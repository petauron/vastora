// Package master owns desired configuration, catalog metadata, and web
// authentication. It has no Docker dependency by design.
package master

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/catalog"
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

func Open(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("master: create data directory: %w", err)
	}
	key, err := secret.LoadOrCreateKey(filepath.Join(dataDir, "master.key"))
	if err != nil {
		return nil, fmt.Errorf("master: load root key: %w", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "master.db"))
	if err != nil {
		return nil, fmt.Errorf("master: open database: %w", err)
	}
	store := &Store{db: db, dataDir: dataDir, key: key, now: time.Now}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value BLOB NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS secrets (
			id TEXT PRIMARY KEY,
			sealed BLOB NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS admins (
			id TEXT PRIMARY KEY,
			password_hash TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token_hash TEXT PRIMARY KEY,
			csrf_token TEXT NOT NULL,
			admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
			expires_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS catalog_sources (
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
		`CREATE TABLE IF NOT EXISTS catalog_cache (
			source_id TEXT PRIMARY KEY REFERENCES catalog_sources(id) ON DELETE CASCADE,
			envelope BLOB NOT NULL,
			etag TEXT NOT NULL DEFAULT '',
			last_modified TEXT NOT NULL DEFAULT '',
			fetched_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS registry_credentials (
			id TEXT PRIMARY KEY,
			host TEXT NOT NULL,
			username TEXT NOT NULL,
			secret_id TEXT NOT NULL REFERENCES secrets(id),
			created_at TEXT NOT NULL
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("master: migrate: %w", err)
		}
	}
	return nil
}

// Initialize creates a single-use bootstrap token only if setup has not
// started. The caller is responsible for displaying it exactly once.
func (s *Store) Initialize(ctx context.Context) (string, error) {
	configured, err := s.IsConfigured(ctx)
	if err != nil {
		return "", err
	}
	if configured {
		return "", errors.New("master: an administrator already exists")
	}
	var existing []byte
	err = s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'bootstrap_token_hash'`).Scan(&existing)
	if err == nil {
		return "", errors.New("master: bootstrap token already exists; initialize only once")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("master: read setup state: %w", err)
	}
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES('bootstrap_token_hash', ?)`, tokenHash(token)); err != nil {
		return "", fmt.Errorf("master: save bootstrap token: %w", err)
	}
	return token, nil
}

func (s *Store) IsConfigured(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&count); err != nil {
		return false, fmt.Errorf("master: read administrators: %w", err)
	}
	return count > 0, nil
}

func (s *Store) SetupStatus(ctx context.Context) (bool, error) {
	configured, err := s.IsConfigured(ctx)
	if err != nil || configured {
		return configured, err
	}
	var token []byte
	err = s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'bootstrap_token_hash'`).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return false, errors.New("master: initialization has not generated a bootstrap token")
	}
	if err != nil {
		return false, fmt.Errorf("master: read setup token: %w", err)
	}
	return false, nil
}

func (s *Store) CreateFirstAdmin(ctx context.Context, bootstrapToken, password string) (string, string, error) {
	if len(password) < 12 {
		return "", "", errors.New("master: password must be at least 12 characters")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", fmt.Errorf("master: begin setup: %w", err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&count); err != nil {
		return "", "", fmt.Errorf("master: read administrators: %w", err)
	}
	if count != 0 {
		return "", "", errors.New("master: setup is already complete")
	}
	var expected []byte
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'bootstrap_token_hash'`).Scan(&expected); err != nil {
		return "", "", errors.New("master: bootstrap token is not available")
	}
	provided := tokenHash(bootstrapToken)
	if subtle.ConstantTimeCompare(expected, provided) != 1 {
		return "", "", errors.New("master: bootstrap token is invalid")
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO admins(id, password_hash, created_at) VALUES(?, ?, ?)`, adminID, passwordHash, now.Format(time.RFC3339Nano)); err != nil {
		return "", "", fmt.Errorf("master: create administrator: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = 'bootstrap_token_hash'`); err != nil {
		return "", "", fmt.Errorf("master: retire bootstrap token: %w", err)
	}
	sessionToken, csrfToken, err := createSession(ctx, tx, adminID, now)
	if err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", fmt.Errorf("master: commit setup: %w", err)
	}
	return sessionToken, csrfToken, nil
}

func (s *Store) Authenticate(ctx context.Context, password string) (string, string, error) {
	var adminID, passwordHash string
	err := s.db.QueryRowContext(ctx, `SELECT id, password_hash FROM admins LIMIT 1`).Scan(&adminID, &passwordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", errors.New("master: setup is not complete")
	}
	if err != nil {
		return "", "", fmt.Errorf("master: read administrator: %w", err)
	}
	if !verifyPassword(password, passwordHash) {
		return "", "", errors.New("master: invalid credentials")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", fmt.Errorf("master: begin session: %w", err)
	}
	defer tx.Rollback()
	token, csrf, err := createSession(ctx, tx, adminID, s.now().UTC())
	if err != nil {
		return "", "", err
	}
	if err := tx.Commit(); err != nil {
		return "", "", fmt.Errorf("master: commit session: %w", err)
	}
	return token, csrf, nil
}

func (s *Store) ValidateSession(ctx context.Context, sessionToken, csrfToken string, mutation bool) error {
	if sessionToken == "" {
		return errors.New("master: authentication required")
	}
	var storedCSRF, expiresAt string
	err := s.db.QueryRowContext(ctx, `SELECT csrf_token, expires_at FROM sessions WHERE token_hash = ?`, tokenHash(sessionToken)).Scan(&storedCSRF, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("master: session is invalid")
	}
	if err != nil {
		return fmt.Errorf("master: read session: %w", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !expires.After(s.now()) {
		return errors.New("master: session has expired")
	}
	if mutation && subtle.ConstantTimeCompare([]byte(storedCSRF), []byte(csrfToken)) != 1 {
		return errors.New("master: CSRF token is invalid")
	}
	return nil
}

func (s *Store) CreateSource(ctx context.Context, input SourceInput) error {
	if !sourceIDPattern.MatchString(input.ID) {
		return errors.New("master: source id must use lowercase letters, digits, and hyphens")
	}
	if strings.TrimSpace(input.DisplayName) == "" || strings.TrimSpace(input.URL) == "" {
		return errors.New("master: source display name and URL are required")
	}
	if len(input.PublicKey) != 32 {
		return errors.New("master: source requires an Ed25519 public key")
	}
	if input.RefreshSeconds == 0 {
		input.RefreshSeconds = 21600
	}
	if input.RefreshSeconds < 300 || input.RefreshSeconds > 604800 {
		return errors.New("master: refresh interval must be between 300 and 604800 seconds")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("master: begin source: %w", err)
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
		return fmt.Errorf("master: create source: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("master: commit source: %w", err)
	}
	return nil
}

func (s *Store) ListSources(ctx context.Context) ([]CatalogSource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id, s.display_name, s.url, s.public_key, s.bearer_secret_id,
		s.custom_ca, s.enabled, s.refresh_seconds, COALESCE(c.fetched_at, ''), s.last_error
		FROM catalog_sources s LEFT JOIN catalog_cache c ON c.source_id = s.id ORDER BY s.id`)
	if err != nil {
		return nil, fmt.Errorf("master: list sources: %w", err)
	}
	defer rows.Close()
	// API consumers distinguish an empty collection from null. Returning an
	// allocated slice keeps the catalog contract stable for a new Master.
	sources := make([]CatalogSource, 0)
	for rows.Next() {
		var source CatalogSource
		var key, bearerID, customCA []byte
		var enabled int
		var fetchedAt string
		if err := rows.Scan(&source.ID, &source.DisplayName, &source.URL, &key, &bearerID, &customCA, &enabled, &source.RefreshSeconds, &fetchedAt, &source.LastError); err != nil {
			return nil, fmt.Errorf("master: scan source: %w", err)
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
		return sourceCredential{}, errors.New("master: catalog source not found")
	}
	if err != nil {
		return sourceCredential{}, fmt.Errorf("master: read source: %w", err)
	}
	if !source.Enabled {
		return sourceCredential{}, errors.New("master: catalog source is disabled")
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
		return fmt.Errorf("master: save catalog: %w", err)
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
		return nil, fmt.Errorf("master: list apps: %w", err)
	}
	defer rows.Close()
	// Keep JSON responses as [] when no verified catalog has been fetched yet.
	apps := make([]AppView, 0)
	for rows.Next() {
		var sourceID, fetchedAt string
		var publicKey, rawEnvelope []byte
		if err := rows.Scan(&sourceID, &publicKey, &rawEnvelope, &fetchedAt); err != nil {
			return nil, fmt.Errorf("master: scan catalog cache: %w", err)
		}
		envelope, err := catalog.ParseEnvelope(rawEnvelope)
		if err != nil {
			return nil, fmt.Errorf("master: cached envelope: %w", err)
		}
		parsedCatalog, _, err := catalog.Verify(envelope, publicKey)
		if err != nil {
			return nil, fmt.Errorf("master: cached catalog: %w", err)
		}
		at, err := time.Parse(time.RFC3339Nano, fetchedAt)
		if err != nil {
			return nil, fmt.Errorf("master: cached catalog timestamp: %w", err)
		}
		for _, app := range parsedCatalog.Apps {
			apps = append(apps, AppView{Key: sourceID + "/" + app.ID, SourceID: sourceID, App: app, FetchedAt: at})
		}
	}
	return apps, rows.Err()
}

func (s *Store) CreateRegistryCredential(ctx context.Context, host, username, token string) (RegistryCredential, error) {
	if strings.TrimSpace(host) == "" || strings.TrimSpace(username) == "" || token == "" {
		return RegistryCredential{}, errors.New("master: registry host, username, and token are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RegistryCredential{}, fmt.Errorf("master: begin registry credential: %w", err)
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
		return RegistryCredential{}, fmt.Errorf("master: create registry credential: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RegistryCredential{}, fmt.Errorf("master: commit registry credential: %w", err)
	}
	return RegistryCredential{ID: id, Host: host, Username: username, TokenSet: true, CreatedAt: now}, nil
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
		return "", fmt.Errorf("master: save secret: %w", err)
	}
	return id, nil
}

func (s *Store) getSecret(ctx context.Context, id, additionalData string) ([]byte, error) {
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE id = ?`, id).Scan(&sealed); err != nil {
		return nil, fmt.Errorf("master: read secret: %w", err)
	}
	value, err := secret.Open(s.key, sealed, []byte(additionalData))
	if err != nil {
		return nil, fmt.Errorf("master: decrypt secret: %w", err)
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
		return "", "", fmt.Errorf("master: create session: %w", err)
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
		return "", fmt.Errorf("master: generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("master: generate password salt: %w", err)
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
