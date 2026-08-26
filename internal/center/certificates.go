package center

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/gateway"
	"golang.org/x/crypto/acme"
)

const (
	privateCertificateRenewBefore = 30 * 24 * time.Hour
	letsencryptAccountID          = "letsencrypt-production"
)

type managedCertificate struct {
	CertificatePEM string    `json:"certificatePem"`
	PrivateKeyPEM  string    `json:"privateKeyPem"`
	NotAfter       time.Time `json:"notAfter"`
}

func (s *Store) obtainPrivateCertificate(ctx context.Context, requestedNames ...string) (managedCertificate, error) {
	cloudflare, err := s.cloudflare(ctx)
	if err != nil {
		return managedCertificate{}, errors.New("center: connect Cloudflare before enabling trusted private HTTPS")
	}
	var zoneName string
	if err := s.db.QueryRowContext(ctx, `SELECT endpoint FROM network_integrations WHERE kind = 'cloudflare' AND status = 'configured'`).Scan(&zoneName); err != nil {
		return managedCertificate{}, errors.New("center: connect Cloudflare before enabling trusted private HTTPS")
	}
	return s.obtainPrivateCertificateWithCloudflare(ctx, cloudflare, zoneName, requestedNames...)
}

func (s *Store) obtainPrivateCertificateWithCloudflare(ctx context.Context, cloudflare cloudflareClient, zoneName string, requestedNames ...string) (managedCertificate, error) {
	names, err := normalizeCertificateDNSNames(requestedNames)
	if err != nil {
		return managedCertificate{}, err
	}
	zoneName = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zoneName), "."))
	if !domainSuffixPattern.MatchString(zoneName) {
		return managedCertificate{}, errors.New("center: Cloudflare Zone is invalid")
	}
	for _, name := range names {
		base := strings.TrimPrefix(name, "*.")
		if base != zoneName && !strings.HasSuffix(base, "."+zoneName) {
			return managedCertificate{}, fmt.Errorf("center: certificate hostname %s is outside the selected Cloudflare Zone %s", name, zoneName)
		}
	}
	s.certificateMu.Lock()
	defer s.certificateMu.Unlock()

	client, err := s.acmeClient(ctx, zoneName)
	if err != nil {
		return managedCertificate{}, err
	}
	identifiers := make([]acme.AuthzID, 0, len(names))
	for _, name := range names {
		identifiers = append(identifiers, acme.AuthzID{Type: "dns", Value: name})
	}
	order, err := client.AuthorizeOrder(ctx, identifiers)
	if err != nil {
		return managedCertificate{}, fmt.Errorf("center: create ACME certificate order: %w", err)
	}
	for _, authorizationURL := range order.AuthzURLs {
		authorization, err := client.GetAuthorization(ctx, authorizationURL)
		if err != nil {
			return managedCertificate{}, fmt.Errorf("center: read ACME authorization: %w", err)
		}
		if authorization.Status == acme.StatusValid {
			continue
		}
		var challenge *acme.Challenge
		for _, candidate := range authorization.Challenges {
			if candidate.Type == "dns-01" {
				challenge = candidate
				break
			}
		}
		if challenge == nil {
			return managedCertificate{}, errors.New("center: certificate authority did not offer DNS validation")
		}
		value, err := client.DNS01ChallengeRecord(challenge.Token)
		if err != nil {
			return managedCertificate{}, fmt.Errorf("center: prepare ACME DNS validation: %w", err)
		}
		identifier := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(authorization.Identifier.Value)), "*.")
		if !domainSuffixPattern.MatchString(identifier) {
			return managedCertificate{}, errors.New("center: certificate authority returned an invalid DNS authorization")
		}
		name := "_acme-challenge." + identifier
		recordID, err := cloudflare.createDNSRecord(ctx, "TXT", name, value, false)
		if err != nil {
			return managedCertificate{}, fmt.Errorf("center: create private HTTPS validation record: %w", err)
		}
		cleanup := func() error { return cloudflare.deleteDNSRecord(context.WithoutCancel(ctx), recordID) }
		if err := waitForPublicTXT(ctx, name, value); err != nil {
			return managedCertificate{}, errors.Join(err, cleanup())
		}
		if _, err := client.Accept(ctx, challenge); err != nil {
			return managedCertificate{}, errors.Join(fmt.Errorf("center: accept ACME DNS validation: %w", err), cleanup())
		}
		if _, err := client.WaitAuthorization(ctx, authorizationURL); err != nil {
			return managedCertificate{}, errors.Join(fmt.Errorf("center: validate private HTTPS hostname: %w", err), cleanup())
		}
		if err := cleanup(); err != nil {
			return managedCertificate{}, fmt.Errorf("center: remove ACME DNS validation record: %w", err)
		}
	}
	ready, err := client.WaitOrder(ctx, order.URI)
	if err != nil {
		return managedCertificate{}, fmt.Errorf("center: wait for ACME certificate order: %w", err)
	}
	certificateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return managedCertificate{}, fmt.Errorf("center: generate private HTTPS key: %w", err)
	}
	requestDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: names[0]}, DNSNames: names}, certificateKey)
	if err != nil {
		return managedCertificate{}, fmt.Errorf("center: create private HTTPS certificate request: %w", err)
	}
	chain, _, err := client.CreateOrderCert(ctx, ready.FinalizeURL, requestDER, true)
	if err != nil {
		return managedCertificate{}, fmt.Errorf("center: issue private HTTPS certificate: %w", err)
	}
	if len(chain) == 0 {
		return managedCertificate{}, errors.New("center: certificate authority returned an empty certificate chain")
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil || !certificateCoversNames(leaf, names) || !leaf.NotAfter.After(s.now().UTC().Add(24*time.Hour)) {
		return managedCertificate{}, errors.New("center: certificate authority returned an invalid private HTTPS certificate")
	}
	var certificatePEM strings.Builder
	for _, certificateDER := range chain {
		if err := pem.Encode(&certificatePEM, &pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}); err != nil {
			return managedCertificate{}, err
		}
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(certificateKey)
	if err != nil {
		return managedCertificate{}, fmt.Errorf("center: encode private HTTPS key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return managedCertificate{CertificatePEM: certificatePEM.String(), PrivateKeyPEM: string(keyPEM), NotAfter: leaf.NotAfter.UTC()}, nil
}

func normalizeCertificateDNSNames(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	names := make([]string, 0, len(values))
	for _, value := range values {
		name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
		base := strings.TrimPrefix(name, "*.")
		if !domainSuffixPattern.MatchString(base) || strings.Contains(base, "*") || strings.HasPrefix(name, "*.") && strings.Count(name, "*") != 1 {
			return nil, errors.New("center: invalid private HTTPS certificate name")
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	if len(names) == 0 || len(names) > 100 {
		return nil, errors.New("center: private HTTPS certificate requires between 1 and 100 DNS names")
	}
	sort.Strings(names)
	return names, nil
}

func certificateCoversNames(certificate *x509.Certificate, names []string) bool {
	for _, name := range names {
		if strings.HasPrefix(name, "*.") {
			found := false
			for _, subject := range certificate.DNSNames {
				if strings.EqualFold(subject, name) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
			continue
		}
		if certificate.VerifyHostname(name) != nil {
			return false
		}
	}
	return true
}

func (s *Store) acmeClient(ctx context.Context, zoneName string) (*acme.Client, error) {
	var accountURI, secretID string
	err := s.db.QueryRowContext(ctx, `SELECT account_uri, secret_id FROM certificate_authorities WHERE id = ?`, letsencryptAccountID).Scan(&accountURI, &secretID)
	if err == nil {
		encoded, err := s.getSecret(ctx, secretID, "acme-account:"+letsencryptAccountID)
		if err != nil {
			return nil, err
		}
		key, err := parseACMEPrivateKey(encoded)
		if err != nil {
			return nil, err
		}
		return &acme.Client{Key: key, KID: acme.KeyID(accountURI), DirectoryURL: acme.LetsEncryptURL, UserAgent: "Vastora/" + Version}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("center: generate ACME account key: %w", err)
	}
	zoneName = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zoneName), "."))
	if !domainSuffixPattern.MatchString(zoneName) {
		return nil, errors.New("center: connect Cloudflare before enabling trusted private HTTPS")
	}
	client := &acme.Client{Key: key, DirectoryURL: acme.LetsEncryptURL, UserAgent: "Vastora/" + Version}
	account, err := client.Register(ctx, &acme.Account{Contact: []string{"mailto:acme@" + zoneName}}, acme.AcceptTOS)
	if err != nil {
		return nil, fmt.Errorf("center: register ACME account: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	secretID, err = s.putSecret(ctx, tx, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), "acme-account:"+letsencryptAccountID)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO certificate_authorities(id, account_uri, secret_id, created_at, updated_at) VALUES(?, ?, ?, ?, ?)`, letsencryptAccountID, account.URI, secretID, now, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	client.KID = acme.KeyID(account.URI)
	return client, nil
}

func parseACMEPrivateKey(encoded []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(encoded)
	if block == nil {
		return nil, errors.New("center: stored ACME account key is invalid")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("center: stored ACME account key is invalid")
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("center: stored ACME account key is invalid")
	}
	return signer, nil
}

func waitForPublicTXT(ctx context.Context, hostname, expected string) error {
	cloudflareResolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "udp", "1.1.1.1:53")
	}}
	resolvers := []*net.Resolver{net.DefaultResolver, cloudflareResolver}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	timeout := time.NewTimer(90 * time.Second)
	defer timeout.Stop()
	for {
		for _, resolver := range resolvers {
			values, err := resolver.LookupTXT(ctx, hostname)
			if err != nil {
				continue
			}
			for _, value := range values {
				if value == expected {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			return errors.New("center: timed out waiting for private HTTPS DNS validation")
		case <-ticker.C:
		}
	}
}

func (s *Store) gatewayCertificates(ctx context.Context, tx *sql.Tx, gatewayID string, state gateway.DesiredState) ([]gateway.Certificate, error) {
	values := []gateway.Certificate{}
	if stateHasRoute(state, "system-center") {
		var endpoint string
		if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, agentConnectURLSetting).Scan(&endpoint); err != nil {
			return nil, err
		}
		hostname, err := gatewayEndpointHostname(endpoint)
		if err != nil {
			return nil, err
		}
		primaryHostname := hostname
		certificate, err := s.loadSystemCenterCertificate(ctx, tx, "", hostname)
		if err != nil {
			return nil, err
		}
		values = append(values, gateway.Certificate{Hostname: hostname, CertificatePEM: certificate.CertificatePEM, PrivateKeyPEM: certificate.PrivateKeyPEM})
		aliases, err := readSystemEndpointAliases(ctx, tx, "center")
		if err != nil {
			return nil, err
		}
		for _, alias := range aliases {
			hostname, err := gatewayEndpointHostname(alias.Endpoint)
			if err != nil {
				return nil, err
			}
			if hostname == primaryHostname {
				continue
			}
			certificate, err := s.loadSystemCenterCertificate(ctx, tx, alias.CertificateSecretID, hostname)
			if err != nil {
				return nil, err
			}
			values = append(values, gateway.Certificate{Hostname: hostname, CertificatePEM: certificate.CertificatePEM, PrivateKeyPEM: certificate.PrivateKeyPEM})
		}
	}
	siteCertificates, err := s.gatewaySiteCertificates(ctx, tx, gatewayID)
	if err != nil {
		return nil, err
	}
	values = append(values, siteCertificates...)
	if err := gateway.ValidateCertificatesForState(state, values); err != nil {
		return nil, err
	}
	return values, nil
}

func (s *Store) RunCertificateRenewal(ctx context.Context, interval time.Duration, report func(error)) {
	if interval < time.Hour {
		interval = 12 * time.Hour
	}
	run := func() {
		if err := s.renewPrivateCertificates(ctx); err != nil && report != nil {
			report(err)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (s *Store) renewPrivateCertificates(ctx context.Context) error {
	var mode, centerEndpoint string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, agentConnectionModeSetting).Scan(&mode); err == nil && mode == "headscale" {
		if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, agentConnectURLSetting).Scan(&centerEndpoint); err != nil {
			return err
		}
		var builtin int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM network_integrations WHERE kind = 'headscale' AND mode = 'builtin' AND status = 'configured'`).Scan(&builtin); err != nil {
			return err
		}
		if builtin == 1 {
			if _, changed, err := s.ensureSystemCenterCertificate(ctx, centerEndpoint); err != nil {
				return err
			} else if changed {
				if err := s.queueAllGatewayStates(ctx); err != nil {
					return err
				}
			}
		}
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return s.renewSiteCertificates(ctx)
}
