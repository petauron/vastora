package center

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/caddyserver/certmagic"
	cfprovider "github.com/libdns/cloudflare"
	"github.com/petauron/vastora/internal/nodesite"
	"github.com/petauron/vastora/internal/secret"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type nodeSiteEngine struct {
	hostname, directory string
	revision            int64
	cache               *certmagic.Cache
	config              *certmagic.Config
	cancel              context.CancelFunc
}

// Reuse Center's OAuth refresh and HTTP policy for every DNS request. No DNS
// credential is embedded in a node task or frozen for a certificate's lifetime.
type nodeSiteDNSClient struct{ store *Store }

func (c nodeSiteDNSClient) Do(request *http.Request) (*http.Response, error) {
	ctx := request.Context()
	cf, err := c.store.cloudflareWithScopes(ctx, "zone.read", "dns.write")
	if err != nil {
		return nil, errors.New("center: Cloudflare DNS authorization unavailable")
	}
	req := request.Clone(ctx)
	// libdns uses the official Cloudflare API. Preserve the existing configured
	// endpoint as well, which the isolated integration fixtures use.
	if req.URL.Scheme != "https" || req.URL.Host != "api.cloudflare.com" || !strings.HasPrefix(req.URL.Path, "/client/v4/") {
		return nil, errors.New("center: unexpected Cloudflare DNS endpoint")
	}
	if cf.baseURL != "" {
		endpoint, err := url.Parse(strings.TrimSuffix(cf.baseURL, "/") + strings.TrimPrefix(req.URL.Path, "/client/v4"))
		if err != nil {
			return nil, err
		}
		endpoint.RawQuery = req.URL.RawQuery
		req.URL = endpoint
	}
	req.Header.Set("Authorization", "Bearer "+cf.token)
	client := cf.http
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return client.Do(req)
}

func (s *Store) newNodeSiteEngine(ctx context.Context, id, hostname, directory string) (*nodeSiteEngine, error) {
	if _, err := nodeSiteDirectory(directory); err != nil {
		return nil, err
	}
	var zone string
	if err := s.db.QueryRowContext(ctx, `SELECT endpoint FROM network_integrations WHERE kind='cloudflare' AND status='configured'`).Scan(&zone); err != nil {
		return nil, errors.New("center: configure Cloudflare DNS first")
	}
	zone = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zone), "."))
	if !nodesite.ValidHostname(zone) || (hostname != zone && !strings.HasSuffix(hostname, "."+zone)) {
		return nil, errors.New("center: hostname is outside the configured Cloudflare zone")
	}
	lifetime, cancel := context.WithCancel(ctx)
	e := &nodeSiteEngine{hostname: hostname, directory: directory, cancel: cancel}
	// ManageAsync also reports startup/storage failures through its logger.
	// Ignore raw messages/fields, but expose a bounded actionable status.
	log := zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(io.Discard), zapcore.ErrorLevel), zap.Hooks(func(zapcore.Entry) error {
		statusCtx, done := context.WithTimeout(lifetime, 2*time.Second)
		defer done()
		_, _ = s.db.ExecContext(statusCtx, `UPDATE node_sites SET error='Certificate management failed; check Center DNS/CA and encrypted certificate storage; the installed website is retained',updated_at=? WHERE application_id=? AND hostname=? AND ca_directory=? AND state IN ('issuing','ready')`, s.now().UTC().Format(time.RFC3339Nano), id, hostname, directory)
		return nil
	}))
	e.cache = certmagic.NewCache(certmagic.CacheOptions{GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) { return e.config, nil }, Logger: log})
	e.config = certmagic.New(e.cache, certmagic.Config{Storage: nodeSiteStorage{store: s}, Logger: log, OnEvent: func(ctx context.Context, event string, _ map[string]any) error {
		if lifetime.Err() != nil {
			return lifetime.Err()
		}
		if event == "cert_failed" {
			// Raw CA/DNS errors can contain request data. Keep public diagnostics bounded.
			_, err := s.db.ExecContext(ctx, `UPDATE node_sites SET error='CertMagic issuance or renewal failed; check Center DNS/CA configuration; the installed website is retained',updated_at=? WHERE application_id=? AND hostname=? AND ca_directory=? AND state IN ('issuing','ready')`, s.now().UTC().Format(time.RFC3339Nano), id, hostname, directory)
			return err
		}
		if event == "cert_obtaining" || event == "cert_renewing" {
			var active bool
			err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM node_sites n JOIN applications a ON a.id=n.application_id WHERE n.application_id=? AND n.hostname=? AND n.ca_directory=? AND n.state IN ('issuing','pending','ready') AND a.status='running' AND a.node_id=n.agent_id)`, id, hostname, directory).Scan(&active)
			if err != nil {
				return err
			}
			if !active {
				return errors.New("center: certificate binding is no longer active")
			}
		}
		return nil
	}})
	issuer := certmagic.NewACMEIssuer(e.config, certmagic.ACMEIssuer{CA: directory, Agreed: true, DisableHTTPChallenge: true, DisableTLSALPNChallenge: true, DNS01Solver: &certmagic.DNS01Solver{DNSManager: certmagic.DNSManager{DNSProvider: &cfprovider.Provider{HTTPClient: nodeSiteDNSClient{store: s}}, Logger: log}}, Logger: log})
	e.config.Issuers = []certmagic.Issuer{issuer}
	if err := e.config.ManageAsync(lifetime, []string{hostname}); err != nil {
		cancel()
		e.cache.Stop()
		return nil, err
	}
	return e, nil
}
func (s *Store) stopNodeSiteEngines() {
	s.nodeSiteMu.Lock()
	defer s.nodeSiteMu.Unlock()
	for id, e := range s.nodeSiteEngines {
		e.cancel()
		e.cache.Stop()
		delete(s.nodeSiteEngines, id)
	}
}

// This poll only reconciles delivery to Agents. CertMagic owns ACME accounts,
// issuance, ARI renewal windows, retries, caching and certificate maintenance.
func (s *Store) runNodeSiteCertificates(ctx context.Context, report func(error)) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	defer s.stopNodeSiteEngines()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := s.advanceNodeSites(ctx); err != nil && report != nil && ctx.Err() == nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Store) advanceNodeSites(ctx context.Context) error {
	s.nodeSiteMu.Lock()
	defer s.nodeSiteMu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT n.application_id,n.hostname,n.ca_directory,n.revision FROM node_sites n JOIN applications a ON a.id=n.application_id WHERE a.node_id=n.agent_id AND a.status='running' AND n.state IN ('issuing','pending','ready')`)
	if err != nil {
		return err
	}
	type binding struct {
		id, host, directory string
		revision            int64
	}
	var bindings []binding
	for rows.Next() {
		var v binding
		if err = rows.Scan(&v.id, &v.host, &v.directory, &v.revision); err != nil {
			rows.Close()
			return err
		}
		bindings = append(bindings, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	active := map[string]bool{}
	for _, v := range bindings {
		active[v.id] = true
	}
	for id, e := range s.nodeSiteEngines {
		if !active[id] {
			e.cancel()
			e.cache.Stop()
			delete(s.nodeSiteEngines, id)
		}
	}
	if s.nodeSiteEngines == nil {
		s.nodeSiteEngines = make(map[string]*nodeSiteEngine)
	}
	for _, v := range bindings {
		e := s.nodeSiteEngines[v.id]
		if e != nil && (e.hostname != v.host || e.directory != v.directory || e.revision != v.revision) {
			e.cancel()
			e.cache.Stop()
			delete(s.nodeSiteEngines, v.id)
			e = nil
		}
		if e == nil {
			e, err = s.newNodeSiteEngine(ctx, v.id, v.host, v.directory)
			if err != nil {
				if _, err = s.db.ExecContext(ctx, `UPDATE node_sites SET error='Certificate management unavailable; check the configured Cloudflare zone and CA',updated_at=? WHERE application_id=? AND hostname=? AND ca_directory=?`, s.now().UTC().Format(time.RFC3339Nano), v.id, v.host, v.directory); err != nil {
					return err
				}
				continue
			}
			e.revision = v.revision
			s.nodeSiteEngines[v.id] = e
		}
		certs := e.cache.AllMatchingCertificates(v.host)
		if len(certs) > 0 {
			newest := certs[0]
			for _, cert := range certs[1:] {
				if cert.Leaf.NotBefore.After(newest.Leaf.NotBefore) {
					newest = cert
				}
			}
			if err = s.deliverNodeSiteCertificate(ctx, v.id, v.host, v.directory, newest.Certificate); err != nil {
				return err
			}
		}
	}
	return nil
}

// A verified managed pair becomes one immutable desired revision. Unresolved
// executions fence all changes, including automatic renewal delivery.
func (s *Store) deliverNodeSiteCertificate(ctx context.Context, id, hostname, directory string, pair tls.Certificate) error {
	if len(pair.Certificate) == 0 {
		return errors.New("center: empty managed certificate")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != hostname || len(leaf.IPAddresses) != 0 || len(leaf.EmailAddresses) != 0 || len(leaf.URIs) != 0 || s.now().Before(leaf.NotBefore) || !s.now().Before(leaf.NotAfter) {
		return errors.New("center: managed certificate does not match the exact node hostname")
	}
	key, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
	var chain []byte
	for _, der := range pair.Certificate {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	if _, err = tls.X509KeyPair(chain, keyPEM); err != nil {
		return errors.New("center: managed certificate key mismatch")
	}
	fingerprint := nodesite.Fingerprint(leaf.Raw)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var agentID, oldFingerprint, state string
	err = tx.QueryRowContext(ctx, `SELECT n.agent_id,n.fingerprint,n.state FROM node_sites n JOIN applications a ON a.id=n.application_id WHERE n.application_id=? AND n.hostname=? AND n.ca_directory=? AND a.node_id=n.agent_id AND a.status='running'`, id, hostname, directory).Scan(&agentID, &oldFingerprint, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if fingerprint == oldFingerprint || state != "issuing" && state != "ready" {
		return nil
	}
	if err = nodeSiteIdle(ctx, tx, id, agentID); err != nil {
		return nil
	}
	sealed, err := secret.Seal(s.key, keyPEM, []byte("node-site-certificate:"+id))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE node_sites SET revision=revision+CASE WHEN fingerprint='' THEN 0 ELSE 1 END,certificate_pem=?,sealed_key=?,fingerprint=?,not_after=?,state='pending',error='',updated_at=? WHERE application_id=?`, string(chain), sealed, fingerprint, leaf.NotAfter.UTC().Format(time.RFC3339Nano), s.now().UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	if err = s.queueNodeSitePhase(ctx, tx, id, "apply"); err != nil {
		return err
	}
	return tx.Commit()
}

// Providers are selected by reviewed directory, never by a node-supplied URL.
// CertMagic's issuer configuration provides the extension point for future CAs.
func nodeSiteDirectory(value string) (string, error) {
	switch value {
	case certmagic.LetsEncryptProductionCA, certmagic.LetsEncryptStagingCA:
		return value, nil
	}
	return "", errors.New("center: unapproved ACME directory")
}
