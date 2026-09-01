// Package center owns desired configuration, catalog metadata, and web
// authentication. It has no Docker dependency by design.
package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/dockerruntime"
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
	cloudflareTunnelMu             sync.Mutex
	assistantProposalMu            sync.Mutex
	assistantResolve               func(context.Context, string) ([]net.IPAddr, error)
	certificateMu                  sync.Mutex
	deploymentCreateMu             sync.Mutex
	domainSwitchMu                 sync.Mutex
	initialSetupMu                 sync.Mutex
	remoteAccessMu                 sync.Mutex
	siteCertificateMu              sync.Mutex
	publicationCleanupMu           sync.Mutex
	publicationVerificationMu      sync.Mutex
	publicationVerificationJobs    map[string]*publicationVerificationJob
	publicationVerificationBackoff func(int) time.Duration
	secretDeliveryMu               sync.Mutex
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
	builtinHeadscaleDialAddress    string
}

// UseHostNetworkAddresses replaces container-namespace discovery with the
// host addresses captured by the official installer. An empty value keeps
// native discovery for non-container development runs.
func (s *Store) UseHostNetworkAddresses(encoded string) error {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil
	}
	type hostAddress struct{ iface, address, kind string }
	values := make([]hostAddress, 0)
	seen := map[string]bool{}
	for _, raw := range strings.Split(encoded, ",") {
		parts := strings.SplitN(strings.TrimSpace(raw), "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return errors.New("center: invalid host network address")
		}
		iface, address := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		ip := net.ParseIP(address)
		kind := networking.Classify(iface, ip)
		if ip == nil || ip.To4() == nil || kind == "" {
			return fmt.Errorf("center: invalid host network address %q", address)
		}
		address = ip.String()
		if seen[address] {
			continue
		}
		seen[address] = true
		values = append(values, hostAddress{iface: iface, address: address, kind: kind})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].kind != values[j].kind {
			return values[i].kind < values[j].kind
		}
		if values[i].iface != values[j].iface {
			return values[i].iface < values[j].iface
		}
		return values[i].address < values[j].address
	})
	s.discoverNetworkCandidates = func(now time.Time) ([]networking.Candidate, error) {
		result := make([]networking.Candidate, 0, len(values))
		for _, value := range values {
			result = append(result, networking.Candidate{Address: value.address, Interface: value.iface, Kind: value.kind, ObservedAt: now.UTC()})
		}
		return result, nil
	}
	return nil
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
	databasePath := filepath.Join(dataDir, "center.db")
	databaseInfo, statErr := os.Stat(databasePath)
	existingDatabase := statErr == nil && databaseInfo.Size() > 0
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("center: inspect database: %w", statErr)
	}
	keyPath := filepath.Join(dataDir, "center.key")
	_, keyStatErr := os.Stat(keyPath)
	keyExists := keyStatErr == nil
	if keyStatErr != nil && !errors.Is(keyStatErr, os.ErrNotExist) {
		return nil, fmt.Errorf("center: inspect root key: %w", keyStatErr)
	}
	if existingDatabase && !keyExists {
		return nil, errors.New("center: existing database requires its original root key")
	}
	if !existingDatabase && keyExists {
		return nil, errors.New("center: root key exists without its database")
	}
	var key []byte
	var err error
	if existingDatabase {
		key, err = secret.LoadKey(keyPath)
	} else {
		key, err = secret.CreateKey(keyPath)
	}
	if err != nil {
		return nil, fmt.Errorf("center: load root key: %w", err)
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
		builtinHeadscaleDialAddress:    net.JoinHostPort(dockerruntime.CaddyAlias, "443"),
		cloudflareOAuth:                defaultCloudflareOAuthConfig(),
		cloudflareOAuthSessions:        make(map[string]*cloudflareOAuthSession),
		assistantResolve:               net.DefaultResolver.LookupIPAddr,
		publicationVerificationJobs:    make(map[string]*publicationVerificationJob),
		publicationVerificationBackoff: defaultPublicationVerificationBackoff,
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
	if existingDatabase {
		bound, err := inspectCenterDatabaseKeyBinding(context.Background(), db, key)
		if err != nil {
			backgroundCancel()
			_ = db.Close()
			return nil, err
		}
		if !bound {
			if err := verifyCenterEncryptedState(context.Background(), db, key); err != nil {
				backgroundCancel()
				_ = db.Close()
				return nil, fmt.Errorf("center: verify legacy encrypted state before binding the database: %w", err)
			}
		}
	}
	if err := store.initializeSchema(context.Background(), existingDatabase); err != nil {
		backgroundCancel()
		_ = db.Close()
		return nil, err
	}
	if err := store.discardEmptyRetiredSharedPublicationMarker(context.Background()); err != nil {
		backgroundCancel()
		_ = db.Close()
		return nil, fmt.Errorf("center: finalize dedicated Web hostname migration: %w", err)
	}
	if err := store.initializeDatabaseKeyBinding(context.Background()); err != nil {
		backgroundCancel()
		_ = db.Close()
		return nil, err
	}
	if err := store.BackfillCatalogManifestHistory(context.Background()); err != nil {
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
	freshComplete = true
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
