package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type PublicationTLSInput struct {
	Enabled *bool `json:"enabled"`
}

type publicationTLSState struct {
	kind, gatewayID, hostname, protocol, status, certificateID string
	enabled                                                    bool
}

// UpdatePublicationTLS changes transport security for an existing private Web
// publication. Certificate issuance happens before the database transaction so
// a failed ACME order leaves the working route untouched.
func (s *Store) UpdatePublicationTLS(ctx context.Context, id string, enabled bool) (PublicationView, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return PublicationView{}, errors.New("center: publication is required")
	}
	current, err := s.publicationTLSState(ctx, id)
	if err != nil {
		return PublicationView{}, err
	}
	if err := validatePublicationTLSState(current); err != nil {
		return PublicationView{}, err
	}
	if enabled == current.enabled && (!enabled || current.certificateID != "") {
		return s.Publication(ctx, id)
	}
	if !enabled {
		return s.applyPublicationTLS(ctx, id, false, nil)
	}

	var zoneName string
	if err := s.db.QueryRowContext(ctx, `SELECT endpoint FROM network_integrations WHERE kind = 'cloudflare' AND status = 'configured'`).Scan(&zoneName); errors.Is(err, sql.ErrNoRows) {
		return PublicationView{}, errors.New("center: connect Cloudflare before enabling trusted private HTTPS")
	} else if err != nil {
		return PublicationView{}, err
	}
	if current.hostname != zoneName && !strings.HasSuffix(current.hostname, "."+zoneName) {
		return PublicationView{}, errors.New("center: private HTTPS hostname must belong to the configured Cloudflare Zone")
	}
	certificate, err := s.obtainPrivateCertificate(ctx, current.hostname)
	if err != nil {
		return PublicationView{}, err
	}
	return s.applyPublicationTLS(ctx, id, true, &certificate)
}

func (s *Store) publicationTLSState(ctx context.Context, id string) (publicationTLSState, error) {
	var value publicationTLSState
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT p.kind, COALESCE(p.gateway_node_id, ''), p.hostname, s.protocol, p.status, p.tls_enabled, COALESCE(p.certificate_secret_id, '')
		FROM publications p JOIN services s ON s.id = p.service_id WHERE p.id = ?`, id).Scan(
		&value.kind, &value.gatewayID, &value.hostname, &value.protocol, &value.status, &enabled, &value.certificateID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return publicationTLSState{}, errors.New("center: publication not found")
	}
	if err != nil {
		return publicationTLSState{}, err
	}
	value.enabled = enabled == 1
	return value, nil
}

func validatePublicationTLSState(value publicationTLSState) error {
	if value.kind != publicationLAN && value.kind != publicationHeadscale {
		return errors.New("center: HTTPS can be changed only for private Web access")
	}
	if value.protocol != "http" && value.protocol != "https" {
		return errors.New("center: HTTPS is available only for Web services")
	}
	if value.status == "stopped" {
		return errors.New("center: stopped access cannot be changed")
	}
	if value.gatewayID == "" {
		return errors.New("center: private access has no entry node")
	}
	return nil
}

func (s *Store) applyPublicationTLS(ctx context.Context, id string, enabled bool, certificate *managedCertificate) (PublicationView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PublicationView{}, err
	}
	defer tx.Rollback()

	var current publicationTLSState
	var currentEnabled int
	if err := tx.QueryRowContext(ctx, `SELECT p.kind, COALESCE(p.gateway_node_id, ''), p.hostname, s.protocol, p.status, p.tls_enabled, COALESCE(p.certificate_secret_id, '')
		FROM publications p JOIN services s ON s.id = p.service_id WHERE p.id = ?`, id).Scan(
		&current.kind, &current.gatewayID, &current.hostname, &current.protocol, &current.status, &currentEnabled, &current.certificateID,
	); errors.Is(err, sql.ErrNoRows) {
		return PublicationView{}, errors.New("center: publication not found")
	} else if err != nil {
		return PublicationView{}, err
	}
	current.enabled = currentEnabled == 1
	if err := validatePublicationTLSState(current); err != nil {
		return PublicationView{}, err
	}
	if enabled == current.enabled && (!enabled || current.certificateID != "") {
		_ = tx.Rollback()
		return s.Publication(ctx, id)
	}

	now := s.now().UTC()
	certificateID, certificateNotAfter := any(nil), ""
	if enabled {
		if certificate == nil || certificate.CertificatePEM == "" || certificate.PrivateKeyPEM == "" || certificate.NotAfter.IsZero() {
			return PublicationView{}, errors.New("center: private HTTPS certificate is invalid")
		}
		encoded, _ := json.Marshal(certificate)
		certificateID, err = s.putSecret(ctx, tx, encoded, "publication-certificate:"+id)
		if err != nil {
			return PublicationView{}, err
		}
		certificateNotAfter = certificate.NotAfter.UTC().Format(time.RFC3339Nano)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE publications SET certificate_secret_id = ?, certificate_not_after = ?, tls_enabled = ?, desired_revision = desired_revision + 1, status = 'pending', last_error = '', updated_at = ? WHERE id = ?`, certificateID, certificateNotAfter, enabled, now.Format(time.RFC3339Nano), id); err != nil {
		return PublicationView{}, err
	}
	updatedRoute, err := tx.ExecContext(ctx, `UPDATE routes SET tls_enabled = ?, status = 'pending', last_error = '', updated_at = ? WHERE publication_id = ?`, enabled, now.Format(time.RFC3339Nano), id)
	if err != nil {
		return PublicationView{}, err
	}
	routes, err := updatedRoute.RowsAffected()
	if err != nil {
		return PublicationView{}, err
	}
	if routes != 1 {
		return PublicationView{}, errors.New("center: private access route not found")
	}
	if err := s.queueGatewayState(ctx, tx, current.gatewayID, now); err != nil {
		return PublicationView{}, err
	}
	if current.certificateID != "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, current.certificateID); err != nil {
			return PublicationView{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return PublicationView{}, err
	}
	return s.Publication(ctx, id)
}
