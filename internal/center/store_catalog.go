package center

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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

// SourceUpdate is a partial update. Secret pointers distinguish preserving an
// existing write-only value (nil) from replacing it (non-empty) or clearing it
// (empty). The source ID and its immutable manifest history never change.
type SourceUpdate struct {
	DisplayName    *string
	URL            *string
	PublicKey      *[]byte
	BearerToken    *string
	CustomCAPEM    *string
	RefreshSeconds *int
	Enabled        *bool
}

type CatalogSource struct {
	ID             string     `json:"id"`
	DisplayName    string     `json:"displayName"`
	URL            string     `json:"url"`
	PublicKey      string     `json:"publicKey"`
	CustomCASet    bool       `json:"customCASet"`
	BearerTokenSet bool       `json:"bearerTokenSet"`
	Enabled        bool       `json:"enabled"`
	Status         string     `json:"status"`
	RefreshSeconds int        `json:"refreshIntervalSeconds"`
	FetchedAt      *time.Time `json:"fetchedAt,omitempty"`
	CheckedAt      *time.Time `json:"checkedAt,omitempty"`
	LastError      string     `json:"lastError,omitempty"`
}

type sourceCredential struct {
	CatalogSource
	publicKey   []byte
	bearerToken string
	customCA    []byte
	etag        string
	lastMod     string
	generation  string
	revision    int64
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
	if input.ID == OfficialCatalogSourceID {
		return errors.New("center: the official catalog namespace is reserved")
	}
	input.URL = strings.TrimSpace(input.URL)
	if strings.TrimSpace(input.DisplayName) == "" || input.URL == "" {
		return errors.New("center: source display name and URL are required")
	}
	parsedURL, err := url.Parse(input.URL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" || parsedURL.User != nil {
		return errors.New("center: source URL must be absolute HTTPS without credentials")
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
	generation, err := randomToken(18)
	if err != nil {
		return fmt.Errorf("center: generate catalog source identity: %w", err)
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
		id, display_name, url, public_key, bearer_secret_id, custom_ca, enabled, refresh_seconds, generation, created_at
	) VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`, input.ID, input.DisplayName, input.URL, input.PublicKey, bearerSecretID, input.CustomCAPEM, input.RefreshSeconds, generation, s.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("center: create source: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("center: commit source: %w", err)
	}
	return nil
}

func (s *Store) UpdateSource(ctx context.Context, id string, update SourceUpdate) error {
	if id == OfficialCatalogSourceID {
		return errors.New("center: the official catalog source is read-only")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("center: begin source update: %w", err)
	}
	defer tx.Rollback()

	var displayName, sourceURL string
	var publicKey, customCA []byte
	var bearerSecretID sql.NullString
	var refreshSeconds, enabled int
	if err := tx.QueryRowContext(ctx, `SELECT display_name, url, public_key, bearer_secret_id, custom_ca, refresh_seconds, enabled
		FROM catalog_sources WHERE id = ?`, id).Scan(&displayName, &sourceURL, &publicKey, &bearerSecretID, &customCA, &refreshSeconds, &enabled); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: catalog source not found")
	} else if err != nil {
		return fmt.Errorf("center: read source for update: %w", err)
	}
	originalURL := sourceURL
	originalKey := append([]byte(nil), publicKey...)
	originalEnabled := enabled
	if update.DisplayName != nil {
		displayName = strings.TrimSpace(*update.DisplayName)
	}
	if update.URL != nil {
		sourceURL = strings.TrimSpace(*update.URL)
	}
	if update.PublicKey != nil {
		publicKey = append([]byte(nil), (*update.PublicKey)...)
	}
	if update.CustomCAPEM != nil {
		customCA = []byte(*update.CustomCAPEM)
	}
	if update.RefreshSeconds != nil {
		refreshSeconds = *update.RefreshSeconds
	}
	if update.Enabled != nil {
		if *update.Enabled {
			enabled = 1
		} else {
			enabled = 0
		}
	}
	if displayName == "" || sourceURL == "" {
		return errors.New("center: source display name and URL are required")
	}
	parsedURL, err := url.Parse(sourceURL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" || parsedURL.User != nil {
		return errors.New("center: source URL must be absolute HTTPS without credentials")
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("center: source requires an Ed25519 public key")
	}
	if refreshSeconds < 300 || refreshSeconds > 604800 {
		return errors.New("center: refresh interval must be between 300 and 604800 seconds")
	}

	newBearerSecretID := bearerSecretID
	if update.BearerToken != nil {
		newBearerSecretID = sql.NullString{}
		if *update.BearerToken != "" {
			secretID, err := s.putSecret(ctx, tx, []byte(*update.BearerToken), "catalog-source:"+id)
			if err != nil {
				return err
			}
			newBearerSecretID = sql.NullString{String: secretID, Valid: true}
		}
	}
	keyChanged := !bytes.Equal(publicKey, originalKey)
	urlChanged := sourceURL != originalURL
	configurationChanged := urlChanged || keyChanged || update.BearerToken != nil || update.CustomCAPEM != nil
	forceRefresh := configurationChanged || originalEnabled == 0 && enabled == 1
	lifecycleChanged := configurationChanged || originalEnabled != enabled
	lastError := ""
	if configurationChanged && !keyChanged {
		lastError = "catalog source configuration changed; refresh required"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_sources SET display_name = ?, url = ?, public_key = ?, bearer_secret_id = ?, custom_ca = ?,
		enabled = ?, refresh_seconds = ?, last_checked_at = CASE WHEN ? THEN '' ELSE last_checked_at END,
		last_error = CASE WHEN ? THEN ? ELSE last_error END,
		revision = revision + CASE WHEN ? THEN 1 ELSE 0 END WHERE id = ?`, displayName, sourceURL, publicKey, nullableSQLString(newBearerSecretID), customCA,
		enabled, refreshSeconds, forceRefresh, configurationChanged, lastError, lifecycleChanged, id); err != nil {
		return fmt.Errorf("center: update source: %w", err)
	}
	if keyChanged {
		if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_cache WHERE source_id = ?`, id); err != nil {
			return fmt.Errorf("center: invalidate source cache after key change: %w", err)
		}
	} else if configurationChanged {
		if _, err := tx.ExecContext(ctx, `UPDATE catalog_cache SET etag = '', last_modified = '' WHERE source_id = ?`, id); err != nil {
			return fmt.Errorf("center: clear source validators after fetch identity change: %w", err)
		}
	}
	if update.BearerToken != nil && bearerSecretID.Valid && (!newBearerSecretID.Valid || newBearerSecretID.String != bearerSecretID.String) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, bearerSecretID.String); err != nil {
			return fmt.Errorf("center: delete replaced catalog credential: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("center: commit source update: %w", err)
	}
	return nil
}

func (s *Store) DeleteSource(ctx context.Context, id string) error {
	if id == OfficialCatalogSourceID {
		return errors.New("center: the official catalog source is read-only")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("center: begin source deletion: %w", err)
	}
	defer tx.Rollback()
	var bearerSecretID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT bearer_secret_id FROM catalog_sources WHERE id = ?`, id).Scan(&bearerSecretID); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: catalog source not found")
	} else if err != nil {
		return fmt.Errorf("center: read source for deletion: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_sources WHERE id = ?`, id); err != nil {
		return fmt.Errorf("center: delete source: %w", err)
	}
	if bearerSecretID.Valid {
		if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, bearerSecretID.String); err != nil {
			return fmt.Errorf("center: delete catalog credential: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("center: commit source deletion: %w", err)
	}
	return nil
}

func (s *Store) ListSources(ctx context.Context) ([]CatalogSource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id, s.display_name, s.url, s.public_key, s.bearer_secret_id,
		s.custom_ca, s.enabled, s.refresh_seconds, COALESCE(c.fetched_at, ''), s.last_checked_at, s.last_error,
		CASE WHEN c.source_id IS NULL THEN 0 ELSE 1 END
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
		var fetchedAt, checkedAt string
		var hasCache int
		if err := rows.Scan(&source.ID, &source.DisplayName, &source.URL, &key, &bearerID, &customCA, &enabled, &source.RefreshSeconds, &fetchedAt, &checkedAt, &source.LastError, &hasCache); err != nil {
			return nil, fmt.Errorf("center: scan source: %w", err)
		}
		source.URL = catalogSourceMetadataURL(source.URL)
		source.PublicKey = base64.RawURLEncoding.EncodeToString(key)
		source.BearerTokenSet = len(bearerID) > 0
		source.CustomCASet = len(customCA) > 0
		source.Enabled = enabled == 1
		if fetchedAt != "" {
			parsed, _ := time.Parse(time.RFC3339Nano, fetchedAt)
			source.FetchedAt = &parsed
		}
		if checkedAt != "" {
			parsed, _ := time.Parse(time.RFC3339Nano, checkedAt)
			source.CheckedAt = &parsed
		}
		source.Status = catalogSourceStatus(source, hasCache == 1, s.now().UTC())
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

func catalogSourceStatus(source CatalogSource, hasCache bool, now time.Time) string {
	if !source.Enabled {
		return "disabled"
	}
	if source.LastError != "" {
		if hasCache {
			return "stale"
		}
		return "failed"
	}
	if !hasCache || source.CheckedAt == nil {
		return "pending"
	}
	if now.After(source.CheckedAt.Add(2 * time.Duration(source.RefreshSeconds) * time.Second)) {
		return "stale"
	}
	return "healthy"
}

func catalogSourceMetadataURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	if parsed.Scheme == "builtin" && parsed.Host == OfficialCatalogSourceID && parsed.User == nil {
		return parsed.String()
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return ""
	}
	parsed.User = nil
	return parsed.String()
}

func (s *Store) SourceForRefresh(ctx context.Context, id string) (sourceCredential, error) {
	var source sourceCredential
	var bearerSecretID sql.NullString
	var cachedSourceID sql.NullString
	var customCA []byte
	err := s.db.QueryRowContext(ctx, `SELECT s.id, s.display_name, s.url, s.public_key, s.bearer_secret_id, s.custom_ca,
		s.enabled, s.refresh_seconds, s.generation, s.revision, COALESCE(c.fetched_at, ''), s.last_error,
		COALESCE(c.etag, ''), COALESCE(c.last_modified, ''), c.source_id
		FROM catalog_sources s LEFT JOIN catalog_cache c ON c.source_id = s.id WHERE s.id = ?`, id).Scan(
		&source.ID, &source.DisplayName, &source.URL, &source.publicKey, &bearerSecretID, &customCA,
		&source.Enabled, &source.RefreshSeconds, &source.generation, &source.revision, new(string), &source.LastError, &source.etag, &source.lastMod, &cachedSourceID)
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

// CommitCatalogRefresh only commits a response fetched from the exact source
// revision that is still enabled. This prevents an in-flight request from
// restoring data after an administrator disables or reconfigures the source.
func (s *Store) CommitCatalogRefresh(ctx context.Context, sourceID, expectedGeneration string, expectedRevision int64, rawEnvelope []byte, etag, lastModified string) error {
	if expectedGeneration == "" || expectedRevision <= 0 {
		return errors.New("center: catalog refresh requires a source generation and revision")
	}
	return s.saveCatalogForRevision(ctx, sourceID, expectedGeneration, expectedRevision, rawEnvelope, etag, lastModified)
}

func (s *Store) saveCatalogForRevision(ctx context.Context, sourceID, expectedGeneration string, expectedRevision int64, rawEnvelope []byte, etag, lastModified string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("center: begin catalog save: %w", err)
	}
	defer tx.Rollback()
	var publicKey []byte
	var enabled int
	var currentGeneration string
	var currentRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT public_key, enabled, generation, revision FROM catalog_sources WHERE id = ?`, sourceID).Scan(&publicKey, &enabled, &currentGeneration, &currentRevision); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: catalog source not found")
	} else if err != nil {
		return fmt.Errorf("center: read source signing key: %w", err)
	}
	if enabled != 1 {
		return errors.New("center: catalog source is disabled")
	}
	if currentGeneration != expectedGeneration || currentRevision != expectedRevision {
		return errors.New("center: catalog source changed during refresh")
	}
	envelope, err := catalog.ParseEnvelope(rawEnvelope)
	if err != nil {
		return err
	}
	verified, _, err := catalog.Verify(envelope, ed25519.PublicKey(publicKey))
	if err != nil {
		return err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if err := recordCatalogManifestHistory(ctx, tx, sourceID, verified, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_cache(source_id, envelope, etag, last_modified, fetched_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(source_id) DO UPDATE SET envelope=excluded.envelope, etag=excluded.etag,
		last_modified=excluded.last_modified, fetched_at=excluded.fetched_at`, sourceID, rawEnvelope, etag, lastModified, now); err != nil {
		return fmt.Errorf("center: save catalog: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE catalog_sources SET last_checked_at = ?, last_error = ''
		WHERE id = ? AND enabled = 1 AND generation = ? AND revision = ?`, now, sourceID, currentGeneration, currentRevision)
	if err != nil {
		return fmt.Errorf("center: mark catalog source healthy: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return fmt.Errorf("center: inspect catalog source revision: %w", err)
		}
		return errors.New("center: catalog source changed during refresh")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("center: commit catalog save: %w", err)
	}
	return nil
}

func (s *Store) RecordCatalogErrorForRevision(ctx context.Context, sourceID, expectedGeneration string, expectedRevision int64, refreshErr error) error {
	if expectedGeneration == "" || expectedRevision <= 0 {
		return errors.New("center: catalog refresh error requires a source generation and revision")
	}
	return s.recordCatalogErrorForRevision(ctx, sourceID, expectedGeneration, expectedRevision, refreshErr)
}

func (s *Store) recordCatalogErrorForRevision(ctx context.Context, sourceID, expectedGeneration string, expectedRevision int64, refreshErr error) error {
	message := "catalog refresh failed"
	if refreshErr != nil {
		message = refreshErr.Error()
	}
	if len(message) > 512 {
		message = message[:512]
	}
	_, err := s.db.ExecContext(ctx, `UPDATE catalog_sources SET last_checked_at = ?, last_error = ?
		WHERE id = ? AND enabled = 1 AND generation = ? AND revision = ?`,
		s.now().UTC().Format(time.RFC3339Nano), message, sourceID, expectedGeneration, expectedRevision)
	return err
}

func (s *Store) MarkCatalogNotModifiedForRevision(ctx context.Context, sourceID, expectedGeneration string, expectedRevision int64, etag, lastModified string) error {
	if expectedGeneration == "" || expectedRevision <= 0 {
		return errors.New("center: catalog revalidation requires a source generation and revision")
	}
	return s.markCatalogNotModifiedForRevision(ctx, sourceID, expectedGeneration, expectedRevision, etag, lastModified)
}

func (s *Store) markCatalogNotModifiedForRevision(ctx context.Context, sourceID, expectedGeneration string, expectedRevision int64, etag, lastModified string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("center: begin unchanged catalog update: %w", err)
	}
	defer tx.Rollback()
	var enabled int
	var currentGeneration string
	var currentRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT enabled, generation, revision FROM catalog_sources WHERE id = ?`, sourceID).Scan(&enabled, &currentGeneration, &currentRevision); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: catalog source not found")
	} else if err != nil {
		return fmt.Errorf("center: read source for revalidation: %w", err)
	}
	if enabled != 1 {
		return errors.New("center: catalog source is disabled")
	}
	if currentGeneration != expectedGeneration || currentRevision != expectedRevision {
		return errors.New("center: catalog source changed during refresh")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE catalog_cache SET etag = ?, last_modified = ?, fetched_at = ? WHERE source_id = ?`, etag, lastModified, now, sourceID)
	if err != nil {
		return fmt.Errorf("center: update unchanged catalog cache: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return fmt.Errorf("center: inspect unchanged catalog cache: %w", err)
		}
		return errors.New("center: catalog returned not modified without a verified cache")
	}
	sourceResult, err := tx.ExecContext(ctx, `UPDATE catalog_sources SET last_checked_at = ?, last_error = ''
		WHERE id = ? AND enabled = 1 AND generation = ? AND revision = ?`, now, sourceID, currentGeneration, currentRevision)
	if err != nil {
		return fmt.Errorf("center: mark unchanged catalog source healthy: %w", err)
	}
	if changed, err := sourceResult.RowsAffected(); err != nil || changed != 1 {
		if err != nil {
			return fmt.Errorf("center: inspect unchanged catalog source revision: %w", err)
		}
		return errors.New("center: catalog source changed during refresh")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("center: commit unchanged catalog update: %w", err)
	}
	return nil
}

func (s *Store) DueCatalogSourceIDs(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM catalog_sources
		WHERE id <> ? AND enabled = 1 AND (
			last_checked_at = '' OR datetime(last_checked_at, '+' || refresh_seconds || ' seconds') <= datetime(?)
		) ORDER BY id`, OfficialCatalogSourceID, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("center: list due catalog sources: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("center: scan due catalog source: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func recordCatalogManifestHistory(ctx context.Context, tx *sql.Tx, sourceID string, value catalog.Catalog, now string) error {
	for _, app := range value.Apps {
		canonical, err := catalog.CanonicalAppManifest(app)
		if err != nil {
			return fmt.Errorf("center: canonicalize immutable catalog manifest %s/%s@%s: %w", sourceID, app.ID, app.Version, err)
		}
		encoded, err := json.Marshal(canonical)
		if err != nil {
			return fmt.Errorf("center: encode immutable catalog manifest %s/%s@%s: %w", sourceID, canonical.ID, canonical.Version, err)
		}
		digest := sha256.Sum256(encoded)
		digestString := hex.EncodeToString(digest[:])
		var existing string
		err = tx.QueryRowContext(ctx, `SELECT manifest_sha256 FROM catalog_manifest_history WHERE source_id = ? AND app_id = ? AND version = ?`, sourceID, canonical.ID, canonical.Version).Scan(&existing)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_manifest_history(source_id, app_id, version, manifest_sha256, first_seen_at) VALUES(?, ?, ?, ?, ?)`, sourceID, canonical.ID, canonical.Version, digestString, now); err != nil {
				return fmt.Errorf("center: record immutable catalog manifest %s/%s@%s: %w", sourceID, canonical.ID, canonical.Version, err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("center: read immutable catalog manifest %s/%s@%s: %w", sourceID, canonical.ID, canonical.Version, err)
		}
		if existing != digestString {
			return fmt.Errorf("center: immutable catalog manifest changed for %s/%s@%s", sourceID, canonical.ID, canonical.Version)
		}
	}
	return nil
}

func (s *Store) BackfillCatalogManifestHistory(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT c.source_id, c.envelope, s.public_key FROM catalog_cache c JOIN catalog_sources s ON s.id = c.source_id ORDER BY c.source_id`)
	if err != nil {
		return fmt.Errorf("center: read cached catalogs for immutable history: %w", err)
	}
	type cachedCatalog struct {
		sourceID string
		catalog  catalog.Catalog
	}
	values := make([]cachedCatalog, 0)
	for rows.Next() {
		var sourceID string
		var rawEnvelope, publicKey []byte
		if err := rows.Scan(&sourceID, &rawEnvelope, &publicKey); err != nil {
			rows.Close()
			return fmt.Errorf("center: scan cached catalog for immutable history: %w", err)
		}
		envelope, err := catalog.ParseEnvelope(rawEnvelope)
		if err != nil {
			rows.Close()
			return fmt.Errorf("center: parse cached catalog %s for immutable history: %w", sourceID, err)
		}
		verified, _, err := catalog.Verify(envelope, ed25519.PublicKey(publicKey))
		if err != nil {
			rows.Close()
			return fmt.Errorf("center: verify cached catalog %s for immutable history: %w", sourceID, err)
		}
		values = append(values, cachedCatalog{sourceID: sourceID, catalog: verified})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("center: iterate cached catalogs for immutable history: %w", err)
	}
	rows.Close()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("center: begin immutable catalog history backfill: %w", err)
	}
	defer tx.Rollback()
	now := s.now().UTC().Format(time.RFC3339Nano)
	for _, value := range values {
		if err := recordCatalogManifestHistory(ctx, tx, value.sourceID, value.catalog, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("center: commit immutable catalog history backfill: %w", err)
	}
	return nil
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
	parsedCatalog, err := catalog.ParseCatalog(payload)
	if err != nil {
		return fmt.Errorf("center: parse official catalog: %w", err)
	}
	envelope, err := catalog.Sign("vastora-official", privateKey, payload)
	if err != nil {
		return fmt.Errorf("center: sign official catalog: %w", err)
	}
	rawEnvelope, err := catalog.MarshalEnvelope(envelope)
	if err != nil {
		return fmt.Errorf("center: marshal official catalog: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("center: begin official catalog save: %w", err)
	}
	defer tx.Rollback()
	var previousBearerSecretID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT bearer_secret_id FROM catalog_sources WHERE id = ?`, OfficialCatalogSourceID).Scan(&previousBearerSecretID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("center: inspect official catalog namespace: %w", err)
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_sources(id, display_name, url, public_key, bearer_secret_id, custom_ca, enabled, refresh_seconds, created_at, last_checked_at, last_error)
		VALUES(?, 'Vastora Official', 'builtin://vastora-official', ?, NULL, NULL, 1, 86400, ?, ?, '')
		ON CONFLICT(id) DO UPDATE SET display_name=excluded.display_name, url=excluded.url, public_key=excluded.public_key,
		bearer_secret_id=NULL, custom_ca=NULL, enabled=1, refresh_seconds=excluded.refresh_seconds,
		last_checked_at=excluded.last_checked_at, last_error=''`, OfficialCatalogSourceID, publicKey, now, now); err != nil {
		return fmt.Errorf("center: save official catalog source: %w", err)
	}
	if err := recordCatalogManifestHistory(ctx, tx, OfficialCatalogSourceID, parsedCatalog, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_cache(source_id, envelope, fetched_at) VALUES(?, ?, ?)
		ON CONFLICT(source_id) DO UPDATE SET envelope=excluded.envelope, fetched_at=excluded.fetched_at`, OfficialCatalogSourceID, rawEnvelope, now); err != nil {
		return fmt.Errorf("center: cache official catalog: %w", err)
	}
	if previousBearerSecretID.Valid {
		if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, previousBearerSecretID.String); err != nil {
			return fmt.Errorf("center: remove credential from reserved official catalog namespace: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("center: commit official catalog: %w", err)
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

func nullableSQLString(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}
