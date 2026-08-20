package center

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/catalog"
)

const OfficialCatalogSourceID = "vastora-official"

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

var sourceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)
