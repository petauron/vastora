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
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/secret"
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

func (s *Store) obtainPrivateCertificate(ctx context.Context, hostname string) (managedCertificate, error) {
	s.certificateMu.Lock()
	defer s.certificateMu.Unlock()

	cloudflare, err := s.cloudflare(ctx)
	if err != nil {
		return managedCertificate{}, errors.New("center: connect Cloudflare before enabling trusted private HTTPS")
	}
	client, err := s.acmeClient(ctx)
	if err != nil {
		return managedCertificate{}, err
	}
	order, err := client.AuthorizeOrder(ctx, []acme.AuthzID{{Type: "dns", Value: hostname}})
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
		name := "_acme-challenge." + hostname
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
	requestDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: hostname}, DNSNames: []string{hostname}}, certificateKey)
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
	if err != nil || leaf.VerifyHostname(hostname) != nil || !leaf.NotAfter.After(s.now().UTC().Add(24*time.Hour)) {
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

func (s *Store) acmeClient(ctx context.Context) (*acme.Client, error) {
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
	var zoneName string
	if err := s.db.QueryRowContext(ctx, `SELECT endpoint FROM network_integrations WHERE kind = 'cloudflare' AND status = 'configured'`).Scan(&zoneName); err != nil {
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

func (s *Store) gatewayCertificates(ctx context.Context, tx *sql.Tx, gatewayID string) ([]gateway.Certificate, error) {
	rows, err := tx.QueryContext(ctx, `SELECT p.id, p.hostname, sec.sealed
		FROM publications p JOIN routes r ON r.publication_id = p.id
		JOIN secrets sec ON sec.id = p.certificate_secret_id
		WHERE r.gateway_node_id = ? AND p.status <> 'stopped' AND p.tls_enabled = 1
		AND p.kind IN ('lan_gateway', 'headscale_gateway') ORDER BY p.hostname`, gatewayID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []gateway.Certificate{}
	for rows.Next() {
		var publicationID, hostname string
		var sealed []byte
		if err := rows.Scan(&publicationID, &hostname, &sealed); err != nil {
			return nil, err
		}
		encoded, err := secret.Open(s.key, sealed, []byte("publication-certificate:"+publicationID))
		if err != nil {
			return nil, fmt.Errorf("center: decrypt private HTTPS certificate: %w", err)
		}
		var certificate managedCertificate
		if json.Unmarshal(encoded, &certificate) != nil || certificate.CertificatePEM == "" || certificate.PrivateKeyPEM == "" {
			return nil, errors.New("center: stored private HTTPS certificate is invalid")
		}
		values = append(values, gateway.Certificate{Hostname: hostname, CertificatePEM: certificate.CertificatePEM, PrivateKeyPEM: certificate.PrivateKeyPEM})
	}
	return values, rows.Err()
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
	cutoff := s.now().UTC().Add(privateCertificateRenewBefore).Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, `SELECT id, hostname, gateway_node_id, COALESCE(certificate_secret_id, '') FROM publications
		WHERE status <> 'stopped' AND tls_enabled = 1 AND kind IN ('lan_gateway', 'headscale_gateway')
		AND (certificate_not_after = '' OR certificate_not_after <= ?) ORDER BY certificate_not_after LIMIT 20`, cutoff)
	if err != nil {
		return err
	}
	type candidate struct{ id, hostname, gatewayID, oldSecretID string }
	values := []candidate{}
	for rows.Next() {
		var value candidate
		if err := rows.Scan(&value.id, &value.hostname, &value.gatewayID, &value.oldSecretID); err != nil {
			rows.Close()
			return err
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var renewalErrors []error
	for _, value := range values {
		certificate, err := s.obtainPrivateCertificate(ctx, value.hostname)
		if err != nil {
			renewalErrors = append(renewalErrors, fmt.Errorf("%s: %w", value.hostname, err))
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		encoded, _ := json.Marshal(certificate)
		secretID, err := s.putSecret(ctx, tx, encoded, "publication-certificate:"+value.id)
		if err == nil {
			var updated sql.Result
			updated, err = tx.ExecContext(ctx, `UPDATE publications SET certificate_secret_id = ?, certificate_not_after = ?, desired_revision = desired_revision + 1, updated_at = ? WHERE id = ? AND status <> 'stopped'`, secretID, certificate.NotAfter.Format(time.RFC3339Nano), s.now().UTC().Format(time.RFC3339Nano), value.id)
			if err == nil {
				changed, changedErr := updated.RowsAffected()
				if changedErr != nil {
					err = changedErr
				} else if changed != 1 {
					_ = tx.Rollback()
					continue
				}
			}
		}
		if err == nil {
			err = s.queueGatewayState(ctx, tx, value.gatewayID, s.now().UTC())
		}
		if err == nil && value.oldSecretID != "" {
			_, err = tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, value.oldSecretID)
		}
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if err != nil {
			renewalErrors = append(renewalErrors, fmt.Errorf("%s: %w", value.hostname, err))
		}
	}
	return errors.Join(renewalErrors...)
}
