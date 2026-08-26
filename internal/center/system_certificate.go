package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/secret"
)

const (
	systemCenterCertificateSecretSetting = "system_center_certificate_secret_id"
	systemCenterCertificateExpirySetting = "system_center_certificate_not_after"
	systemCenterCertificateContext       = "system-certificate:center"
)

func (s *Store) ensureSystemCenterCertificate(ctx context.Context, endpoint string) (managedCertificate, bool, error) {
	hostname, err := gatewayEndpointHostname(endpoint)
	if err != nil {
		return managedCertificate{}, false, fmt.Errorf("center: private Center certificate hostname: %w", err)
	}
	var secretID, expiry string
	secretErr := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, systemCenterCertificateSecretSetting).Scan(&secretID)
	expiryErr := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, systemCenterCertificateExpirySetting).Scan(&expiry)
	if secretErr == nil && expiryErr == nil {
		notAfter, parseErr := time.Parse(time.RFC3339Nano, expiry)
		if parseErr == nil && notAfter.After(s.now().UTC().Add(privateCertificateRenewBefore)) {
			certificate, loadErr := s.loadSystemCenterCertificate(ctx, s.db, secretID, hostname)
			if loadErr == nil {
				return certificate, false, nil
			}
		}
	} else if (!errors.Is(secretErr, sql.ErrNoRows) && secretErr != nil) || (!errors.Is(expiryErr, sql.ErrNoRows) && expiryErr != nil) {
		return managedCertificate{}, false, errors.Join(secretErr, expiryErr)
	}

	certificate, err := s.obtainPrivateCertificate(ctx, hostname)
	if err != nil {
		return managedCertificate{}, false, err
	}
	encoded, err := json.Marshal(certificate)
	if err != nil {
		return managedCertificate{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return managedCertificate{}, false, err
	}
	defer tx.Rollback()
	newSecretID, err := s.putSecret(ctx, tx, encoded, systemCenterCertificateContext)
	if err != nil {
		return managedCertificate{}, false, err
	}
	for key, value := range map[string]string{
		systemCenterCertificateSecretSetting: newSecretID,
		systemCenterCertificateExpirySetting: certificate.NotAfter.Format(time.RFC3339Nano),
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			return managedCertificate{}, false, err
		}
	}
	if secretID != "" && secretID != newSecretID {
		var aliases int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM system_endpoint_aliases WHERE certificate_secret_id = ?`, secretID).Scan(&aliases); err != nil {
			return managedCertificate{}, false, err
		}
		if aliases == 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, secretID); err != nil {
				return managedCertificate{}, false, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return managedCertificate{}, false, err
	}
	return certificate, true, nil
}

type systemCertificateQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) loadSystemCenterCertificate(ctx context.Context, query systemCertificateQuerier, secretID, hostname string) (managedCertificate, error) {
	if secretID == "" {
		if err := query.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, systemCenterCertificateSecretSetting).Scan(&secretID); err != nil {
			return managedCertificate{}, err
		}
	}
	var sealed []byte
	if err := query.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE id = ?`, secretID).Scan(&sealed); err != nil {
		return managedCertificate{}, err
	}
	encoded, err := secret.Open(s.key, sealed, []byte(systemCenterCertificateContext))
	if err != nil {
		return managedCertificate{}, err
	}
	var certificate managedCertificate
	if json.Unmarshal(encoded, &certificate) != nil {
		return managedCertificate{}, errors.New("center: stored private Center certificate is invalid")
	}
	if err := gateway.ValidateCertificates([]gateway.Certificate{{Hostname: hostname, CertificatePEM: certificate.CertificatePEM, PrivateKeyPEM: certificate.PrivateKeyPEM}}); err != nil {
		return managedCertificate{}, err
	}
	return certificate, nil
}

func stateHasRoute(state gateway.DesiredState, id string) bool {
	for _, route := range state.Routes {
		if route.ID == id {
			return true
		}
	}
	return false
}
