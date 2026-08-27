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

	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/secret"
	_ "modernc.org/sqlite"
)

type Store struct {
	db                             *sql.DB
	dataDir                        string
	key                            []byte
	backgroundCtx                  context.Context
	backgroundCancel               context.CancelFunc
	backgroundMu                   sync.Mutex
	backgroundClosed               bool
	backgroundWG                   sync.WaitGroup
	headscaleAllowedEndpoints      []string
	headscaleHTTPClient            *http.Client
	cloudflareOAuth                cloudflareOAuthConfig
	cloudflareOAuthMu              sync.Mutex
	cloudflareOAuthSessions        map[string]*cloudflareOAuthSession
	cloudflareTokenMu              sync.Mutex
	certificateMu                  sync.Mutex
	domainSwitchMu                 sync.Mutex
	siteCertificateMu              sync.Mutex
	publicationCleanupMu           sync.Mutex
	publicationVerificationMu      sync.Mutex
	publicationVerificationJobs    map[string]*publicationVerificationJob
	publicationVerificationBackoff func(int) time.Duration
	agentDecommissionGrace         time.Duration
	verifyPublication              func(context.Context, string, int64) (PublicationView, error)
	taskChanges                    changeNotifier
	now                            func() time.Time
	discoverNetworkCandidates      func(time.Time) ([]networking.Candidate, error)
	lookupPublicRegion             func(context.Context, string) (string, error)
	lookupPublicAddress            func(context.Context) (string, error)
	lookupGatewayAddress           func(string) (string, error)
	verifyPublicEntry              func(context.Context, string, deployapi.PublicEntryProbe) error
	issuePrivateCertificate        func(context.Context, ...string) (managedCertificate, error)
	issueDomainCertificate         func(context.Context, cloudflareClient, string, ...string) (managedCertificate, error)
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
	backgroundCtx, backgroundCancel := context.WithCancel(context.Background())
	store := &Store{
		db: db, dataDir: dataDir, key: key,
		backgroundCtx:                  backgroundCtx,
		backgroundCancel:               backgroundCancel,
		headscaleAllowedEndpoints:      headscaleAllowedEndpoints,
		headscaleHTTPClient:            &http.Client{Timeout: 20 * time.Second},
		cloudflareOAuth:                defaultCloudflareOAuthConfig(),
		cloudflareOAuthSessions:        make(map[string]*cloudflareOAuthSession),
		publicationVerificationJobs:    make(map[string]*publicationVerificationJob),
		publicationVerificationBackoff: defaultPublicationVerificationBackoff,
		agentDecommissionGrace:         45 * time.Second,
		now:                            time.Now,
		discoverNetworkCandidates:      networking.Discover,
		lookupPublicRegion: countryISLookup(&http.Client{
			Timeout: 8 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}),
		lookupPublicAddress:  vastoraPublicAddressLookup(&http.Client{Timeout: 5 * time.Second}),
		lookupGatewayAddress: networking.DefaultRouteAddress,
		verifyPublicEntry:    vastoraPublicEntryVerifier(&http.Client{Timeout: 12 * time.Second}),
	}
	store.verifyPublication = store.verifyPublicationRevision
	store.issuePrivateCertificate = store.obtainPrivateCertificate
	store.issueDomainCertificate = store.obtainPrivateCertificateWithCloudflare
	if err := store.initializeSchema(context.Background(), existingDatabase); err != nil {
		backgroundCancel()
		_ = db.Close()
		return nil, err
	}
	if err := store.ensureHeadscaleDNSFile(); err != nil {
		backgroundCancel()
		_ = db.Close()
		return nil, err
	}
	if err := store.enforceThreeXUIWorkerIsolation(context.Background()); err != nil {
		backgroundCancel()
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	s.backgroundMu.Lock()
	if !s.backgroundClosed {
		s.backgroundClosed = true
		if s.backgroundCancel != nil {
			s.backgroundCancel()
		}
	}
	s.backgroundMu.Unlock()
	s.backgroundWG.Wait()
	return s.db.Close()
}

func (s *Store) startBackground(run func()) bool {
	s.backgroundMu.Lock()
	defer s.backgroundMu.Unlock()
	if s.backgroundClosed {
		return false
	}
	s.backgroundWG.Add(1)
	go func() {
		defer s.backgroundWG.Done()
		run()
	}()
	return true
}
