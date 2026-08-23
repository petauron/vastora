package center

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/petauron/vastora/internal/secret"
)

type legacyPublicationCertificate struct {
	publicationID string
	hostname      string
	sealed        []byte
}

type stagedSiteCertificate struct {
	siteID, secretID, notAfter, createdAt string
	dnsNames, sealed                      []byte
}

// stageLegacySiteCertificates runs while the v14 columns and secrets still
// exist. The handoff table persists across a stopped upgrade, and v16 adopts
// its already re-sealed values in the same migration that removes the table.
func (s *Store) stageLegacySiteCertificates(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT svc.site_id, p.id, p.hostname, sec.sealed
		FROM publications p JOIN services svc ON svc.id = p.service_id
		JOIN secrets sec ON sec.id = p.certificate_secret_id
		WHERE p.status <> 'stopped' AND p.tls_enabled = 1
		AND p.kind IN ('lan_gateway', 'headscale_gateway')
		AND p.certificate_secret_id IS NOT NULL
		ORDER BY svc.site_id, p.hostname, p.id`)
	if err != nil {
		return err
	}
	grouped := map[string][]legacyPublicationCertificate{}
	for rows.Next() {
		var siteID string
		var value legacyPublicationCertificate
		if err := rows.Scan(&siteID, &value.publicationID, &value.hostname, &value.sealed); err != nil {
			rows.Close()
			return err
		}
		grouped[siteID] = append(grouped[siteID], value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	siteIDs := make([]string, 0, len(grouped))
	for siteID := range grouped {
		siteIDs = append(siteIDs, siteID)
	}
	sort.Strings(siteIDs)
	staged := make([]stagedSiteCertificate, 0, len(siteIDs))
	for _, siteID := range siteIDs {
		values := grouped[siteID]
		hostnames := make([]string, 0, len(values))
		for _, value := range values {
			hostnames = append(hostnames, value.hostname)
		}
		dnsNames, err := normalizeCertificateDNSNames(hostnames)
		if err != nil {
			return fmt.Errorf("center: prepare Site certificate migration for %s: %w", siteID, err)
		}
		certificate, found := s.reusableLegacySiteCertificate(values, dnsNames)
		if !found {
			if s.issuePrivateCertificate == nil {
				return errors.New("center: private HTTPS certificate issuer is unavailable during migration")
			}
			certificate, err = s.issuePrivateCertificate(ctx, dnsNames...)
			if err != nil {
				return fmt.Errorf("center: issue replacement Site certificate before migration: %w", err)
			}
		}
		encodedCertificate, err := json.Marshal(certificate)
		if err != nil {
			return err
		}
		sealed, err := secret.Seal(s.key, encodedCertificate, []byte("site-certificate:"+siteID))
		if err != nil {
			return err
		}
		secretID, err := randomToken(18)
		if err != nil {
			return err
		}
		encodedNames, err := json.Marshal(dnsNames)
		if err != nil {
			return err
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		staged = append(staged, stagedSiteCertificate{
			siteID: siteID, secretID: secretID, dnsNames: encodedNames, sealed: sealed,
			notAfter: certificate.NotAfter.UTC().Format(time.RFC3339Nano), createdAt: now,
		})
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS site_certificate_handoff (
		site_id TEXT PRIMARY KEY,
		dns_names_json BLOB NOT NULL,
		secret_id TEXT NOT NULL UNIQUE,
		sealed BLOB NOT NULL,
		not_after TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	for _, value := range staged {
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_certificate_handoff(site_id, dns_names_json, secret_id, sealed, not_after, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(site_id) DO UPDATE SET dns_names_json = excluded.dns_names_json, secret_id = excluded.secret_id,
			sealed = excluded.sealed, not_after = excluded.not_after, updated_at = excluded.updated_at`,
			value.siteID, value.dnsNames, value.secretID, value.sealed, value.notAfter, value.createdAt, value.createdAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) reusableLegacySiteCertificate(values []legacyPublicationCertificate, dnsNames []string) (managedCertificate, bool) {
	for _, value := range values {
		encoded, err := secret.Open(s.key, value.sealed, []byte("publication-certificate:"+value.publicationID))
		if err != nil {
			continue
		}
		var certificate managedCertificate
		if json.Unmarshal(encoded, &certificate) != nil || !managedCertificateCovers(certificate, dnsNames, s.now().UTC()) {
			continue
		}
		return certificate, true
	}
	return managedCertificate{}, false
}

func managedCertificateCovers(certificate managedCertificate, dnsNames []string, now time.Time) bool {
	if certificate.CertificatePEM == "" || certificate.PrivateKeyPEM == "" {
		return false
	}
	if _, err := tls.X509KeyPair([]byte(certificate.CertificatePEM), []byte(certificate.PrivateKeyPEM)); err != nil {
		return false
	}
	block, _ := pem.Decode([]byte(certificate.CertificatePEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return false
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	return err == nil && leaf.NotAfter.After(now) && certificateCoversNames(leaf, dnsNames)
}

func (s *Store) activateMigratedSiteCertificates(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT site_id FROM site_certificates WHERE last_error = 'migration handoff pending' ORDER BY site_id`)
	if err != nil {
		return err
	}
	var siteIDs []string
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
	now := s.now().UTC()
	for _, siteID := range siteIDs {
		gatewayIDs, err := siteTLSGateways(ctx, tx, siteID)
		if err != nil {
			return err
		}
		for _, gatewayID := range gatewayIDs {
			if err := s.queueGatewayState(ctx, tx, gatewayID, now); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE site_certificates SET last_error = '', updated_at = ? WHERE site_id = ?`, now.Format(time.RFC3339Nano), siteID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
