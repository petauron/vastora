package center

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/secret"
)

type storedSiteCertificate struct {
	siteID, secretID, notAfter string
	dnsNames                   []string
	sealed                     []byte
}

func (s *Store) ensureSiteCertificateForHostname(ctx context.Context, siteID, hostname string) error {
	return s.ensureSiteCertificate(ctx, siteID, hostname)
}

func (s *Store) ensureSiteCertificate(ctx context.Context, siteID, candidateHostname string) error {
	s.siteCertificateMu.Lock()
	defer s.siteCertificateMu.Unlock()

	dnsNames, err := s.siteCertificateDNSNames(ctx, siteID, candidateHostname)
	if err != nil {
		return err
	}
	current, err := s.storedSiteCertificate(ctx, siteID)
	if err == nil && slices.Equal(current.dnsNames, dnsNames) {
		notAfter, parseErr := time.Parse(time.RFC3339Nano, current.notAfter)
		if parseErr == nil && notAfter.After(s.now().UTC().Add(privateCertificateRenewBefore)) {
			if _, loadErr := s.decodeSiteCertificate(current); loadErr == nil {
				return nil
			}
		}
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	if s.issuePrivateCertificate == nil {
		return errors.New("center: private HTTPS certificate issuer is unavailable")
	}
	certificate, issueErr := s.issuePrivateCertificate(ctx, dnsNames...)
	if issueErr != nil {
		return errors.Join(issueErr, s.recordSiteCertificateFailure(ctx, siteID, dnsNames, issueErr))
	}
	return s.storeSiteCertificate(ctx, siteID, dnsNames, certificate, current.secretID)
}

func (s *Store) siteCertificateDNSNames(ctx context.Context, siteID, candidateHostname string) ([]string, error) {
	var code, domainSuffix, zoneName string
	if err := s.db.QueryRowContext(ctx, `SELECT code, domain_suffix FROM sites WHERE id = ? AND status = 'active'`, strings.TrimSpace(siteID)).Scan(&code, &domainSuffix); errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("center: Site was not found for private HTTPS")
	} else if err != nil {
		return nil, err
	}
	siteBase, err := siteDomainBase(code, domainSuffix)
	if err != nil {
		return nil, errors.New("center: Site requires a valid domain namespace for private HTTPS")
	}
	if err := s.db.QueryRowContext(ctx, `SELECT endpoint FROM network_integrations WHERE kind = 'cloudflare' AND status = 'configured'`).Scan(&zoneName); errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("center: connect Cloudflare before enabling trusted private HTTPS")
	} else if err != nil {
		return nil, err
	}
	zoneName = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zoneName), "."))
	if !domainSuffixPattern.MatchString(zoneName) || (siteBase != zoneName && !strings.HasSuffix(siteBase, "."+zoneName)) {
		return nil, errors.New("center: Site domain namespace must belong to the configured Cloudflare Zone")
	}

	hostnames := []string{strings.TrimSpace(candidateHostname)}
	rows, err := s.db.QueryContext(ctx, `SELECT p.hostname FROM publications p
		JOIN services s ON s.id = p.service_id
		WHERE s.site_id = ? AND p.status <> 'stopped' AND p.tls_enabled = 1
		AND p.kind IN ('lan_gateway', 'headscale_gateway') ORDER BY p.hostname`, siteID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var hostname string
		if err := rows.Scan(&hostname); err != nil {
			rows.Close()
			return nil, err
		}
		hostnames = append(hostnames, hostname)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	dnsNames := []string{"*." + siteBase}
	for _, hostname := range hostnames {
		hostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
		if hostname == "" {
			continue
		}
		if err := validateHostnameInSiteNamespace(hostname, code, domainSuffix); err != nil {
			return nil, err
		}
		if hostname != zoneName && !strings.HasSuffix(hostname, "."+zoneName) {
			return nil, errors.New("center: private HTTPS hostname must belong to the configured Cloudflare Zone")
		}
		if hostname == siteBase {
			dnsNames = append(dnsNames, hostname)
			continue
		}
		dot := strings.IndexByte(hostname, '.')
		if dot < 1 || dot == len(hostname)-1 {
			return nil, errors.New("center: private HTTPS hostname cannot use a shared Site certificate")
		}
		dnsNames = append(dnsNames, "*."+hostname[dot+1:])
	}
	return normalizeCertificateDNSNames(dnsNames)
}

func (s *Store) storedSiteCertificate(ctx context.Context, siteID string) (storedSiteCertificate, error) {
	var value storedSiteCertificate
	var encoded []byte
	err := s.db.QueryRowContext(ctx, `SELECT c.site_id, c.dns_names_json, COALESCE(c.secret_id, ''), c.not_after, COALESCE(sec.sealed, X'')
		FROM site_certificates c LEFT JOIN secrets sec ON sec.id = c.secret_id WHERE c.site_id = ?`, siteID).Scan(
		&value.siteID, &encoded, &value.secretID, &value.notAfter, &value.sealed,
	)
	if err != nil {
		return storedSiteCertificate{}, err
	}
	if json.Unmarshal(encoded, &value.dnsNames) != nil {
		return storedSiteCertificate{}, errors.New("center: stored Site certificate names are invalid")
	}
	value.dnsNames, err = normalizeCertificateDNSNames(value.dnsNames)
	if err != nil {
		return storedSiteCertificate{}, err
	}
	return value, nil
}

func (s *Store) decodeSiteCertificate(value storedSiteCertificate) (managedCertificate, error) {
	if value.secretID == "" || len(value.sealed) == 0 {
		return managedCertificate{}, errors.New("center: Site certificate is not ready")
	}
	encoded, err := secret.Open(s.key, value.sealed, []byte("site-certificate:"+value.siteID))
	if err != nil {
		return managedCertificate{}, fmt.Errorf("center: decrypt Site certificate: %w", err)
	}
	var certificate managedCertificate
	if json.Unmarshal(encoded, &certificate) != nil || certificate.CertificatePEM == "" || certificate.PrivateKeyPEM == "" {
		return managedCertificate{}, errors.New("center: stored Site certificate is invalid")
	}
	block, _ := pem.Decode([]byte(certificate.CertificatePEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return managedCertificate{}, errors.New("center: stored Site certificate is invalid")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !certificateCoversNames(leaf, value.dnsNames) || !leaf.NotAfter.After(s.now().UTC()) {
		return managedCertificate{}, errors.New("center: stored Site certificate does not cover its DNS names")
	}
	return certificate, nil
}

func (s *Store) storeSiteCertificate(ctx context.Context, siteID string, dnsNames []string, certificate managedCertificate, previousSecretID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	encodedCertificate, _ := json.Marshal(certificate)
	secretID, err := s.putSecret(ctx, tx, encodedCertificate, "site-certificate:"+siteID)
	if err != nil {
		return err
	}
	encodedNames, _ := json.Marshal(dnsNames)
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_certificates(site_id, dns_names_json, secret_id, not_after, status, last_error, created_at, updated_at)
		VALUES(?, ?, ?, ?, 'ready', '', ?, ?)
		ON CONFLICT(site_id) DO UPDATE SET dns_names_json = excluded.dns_names_json, secret_id = excluded.secret_id,
		not_after = excluded.not_after, status = 'ready', last_error = '', updated_at = excluded.updated_at`,
		siteID, encodedNames, secretID, certificate.NotAfter.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'pending', last_error = '', desired_revision = desired_revision + 1, updated_at = ?
		WHERE service_id IN (SELECT id FROM services WHERE site_id = ?) AND status <> 'stopped' AND tls_enabled = 1
		AND kind IN ('lan_gateway', 'headscale_gateway')`, now.Format(time.RFC3339Nano), siteID); err != nil {
		return err
	}
	gateways, err := siteTLSGateways(ctx, tx, siteID)
	if err != nil {
		return err
	}
	for _, gatewayID := range gateways {
		if err := s.queueGatewayState(ctx, tx, gatewayID, now); err != nil {
			return err
		}
	}
	if previousSecretID != "" && previousSecretID != secretID {
		if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, previousSecretID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) recordSiteCertificateFailure(ctx context.Context, siteID string, dnsNames []string, issueErr error) error {
	encodedNames, _ := json.Marshal(dnsNames)
	now := s.now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO site_certificates(site_id, dns_names_json, status, last_error, created_at, updated_at)
		VALUES(?, ?, 'failed', ?, ?, ?)
		ON CONFLICT(site_id) DO UPDATE SET status = 'failed', last_error = excluded.last_error, updated_at = excluded.updated_at`,
		siteID, encodedNames, issueErr.Error(), now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'degraded', last_error = ?, updated_at = ?
		WHERE service_id IN (SELECT id FROM services WHERE site_id = ?) AND status <> 'stopped' AND tls_enabled = 1
		AND kind IN ('lan_gateway', 'headscale_gateway')`, issueErr.Error(), now, siteID); err != nil {
		return err
	}
	return tx.Commit()
}

func siteTLSGateways(ctx context.Context, tx *sql.Tx, siteID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT r.gateway_node_id FROM routes r
		JOIN publications p ON p.id = r.publication_id JOIN services s ON s.id = p.service_id
		WHERE s.site_id = ? AND p.status <> 'stopped' AND p.tls_enabled = 1
		AND p.kind IN ('lan_gateway', 'headscale_gateway') ORDER BY r.gateway_node_id`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) gatewaySiteCertificates(ctx context.Context, tx *sql.Tx, gatewayID string) ([]gateway.Certificate, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT c.site_id, c.dns_names_json, COALESCE(c.secret_id, ''), c.not_after, COALESCE(sec.sealed, X'')
		FROM routes r JOIN publications p ON p.id = r.publication_id JOIN services svc ON svc.id = p.service_id
		JOIN site_certificates c ON c.site_id = svc.site_id LEFT JOIN secrets sec ON sec.id = c.secret_id
		WHERE r.gateway_node_id = ? AND p.status <> 'stopped' AND p.tls_enabled = 1
		AND p.kind IN ('lan_gateway', 'headscale_gateway') ORDER BY c.site_id`, gatewayID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []gateway.Certificate{}
	for rows.Next() {
		var stored storedSiteCertificate
		var encodedNames []byte
		if err := rows.Scan(&stored.siteID, &encodedNames, &stored.secretID, &stored.notAfter, &stored.sealed); err != nil {
			return nil, err
		}
		if json.Unmarshal(encodedNames, &stored.dnsNames) != nil {
			return nil, errors.New("center: stored Site certificate names are invalid")
		}
		stored.dnsNames, err = normalizeCertificateDNSNames(stored.dnsNames)
		if err != nil {
			return nil, err
		}
		certificate, err := s.decodeSiteCertificate(stored)
		if err != nil {
			return nil, err
		}
		values = append(values, gateway.Certificate{Hostname: stored.dnsNames[0], CertificatePEM: certificate.CertificatePEM, PrivateKeyPEM: certificate.PrivateKeyPEM})
	}
	return values, rows.Err()
}

func (s *Store) renewSiteCertificates(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT s.site_id FROM services s JOIN publications p ON p.service_id = s.id
		WHERE p.status <> 'stopped' AND p.tls_enabled = 1 AND p.kind IN ('lan_gateway', 'headscale_gateway') ORDER BY s.site_id`)
	if err != nil {
		return err
	}
	siteIDs := []string{}
	for rows.Next() {
		var siteID string
		if err := rows.Scan(&siteID); err != nil {
			rows.Close()
			return err
		}
		siteIDs = append(siteIDs, siteID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var renewalErrors []error
	for _, siteID := range siteIDs {
		if err := s.ensureSiteCertificate(ctx, siteID, ""); err != nil {
			renewalErrors = append(renewalErrors, fmt.Errorf("Site %s: %w", siteID, err))
		}
	}
	return errors.Join(renewalErrors...)
}
