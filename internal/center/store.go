// Package center owns desired configuration, catalog metadata, and web
// authentication. It has no Docker dependency by design.
package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/secret"
	_ "modernc.org/sqlite"
)

type Store struct {
	db                        *sql.DB
	dataDir                   string
	key                       []byte
	headscaleAllowedEndpoints []string
	headscaleHTTPClient       *http.Client
	cloudflareOAuth           cloudflareOAuthConfig
	cloudflareOAuthMu         sync.Mutex
	cloudflareOAuthSessions   map[string]*cloudflareOAuthSession
	cloudflareTokenMu         sync.Mutex
	certificateMu             sync.Mutex
	publicationCleanupMu      sync.Mutex
	now                       func() time.Time
	discoverNetworkCandidates func(time.Time) ([]networking.Candidate, error)
}

func Open(dataDir string, headscaleAllowedURLs ...string) (*Store, error) {
	headscaleAllowedEndpoints := make([]string, 0, len(headscaleAllowedURLs))
	seenHeadscaleEndpoints := make(map[string]struct{}, len(headscaleAllowedURLs))
	for _, value := range headscaleAllowedURLs {
		endpoint, err := normalizeHeadscaleEndpoint(value)
		if err != nil {
			return nil, fmt.Errorf("center: invalid allowed Headscale URL: %w", err)
		}
		if _, exists := seenHeadscaleEndpoints[endpoint]; exists {
			continue
		}
		seenHeadscaleEndpoints[endpoint] = struct{}{}
		headscaleAllowedEndpoints = append(headscaleAllowedEndpoints, endpoint)
	}
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
	store := &Store{
		db: db, dataDir: dataDir, key: key,
		headscaleAllowedEndpoints: headscaleAllowedEndpoints,
		headscaleHTTPClient:       &http.Client{Timeout: 20 * time.Second},
		cloudflareOAuth:           defaultCloudflareOAuthConfig(),
		cloudflareOAuthSessions:   make(map[string]*cloudflareOAuthSession),
		now:                       time.Now,
		discoverNetworkCandidates: networking.Discover,
	}
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
