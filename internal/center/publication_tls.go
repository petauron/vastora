package center

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type PublicationTLSInput struct {
	Enabled *bool `json:"enabled"`
}

type publicationTLSState struct {
	kind, gatewayID, hostname, protocol, status, siteID string
	enabled                                             bool
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
	if enabled == current.enabled {
		return s.Publication(ctx, id)
	}
	if !enabled {
		return s.applyPublicationTLS(ctx, id, false)
	}
	if err := s.ensureSiteCertificateForHostname(ctx, current.siteID, current.hostname); err != nil {
		return PublicationView{}, err
	}
	return s.applyPublicationTLS(ctx, id, true)
}

func (s *Store) publicationTLSState(ctx context.Context, id string) (publicationTLSState, error) {
	var value publicationTLSState
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT p.kind, COALESCE(p.gateway_node_id, ''), p.hostname, s.protocol, p.status, p.tls_enabled, s.site_id
		FROM publications p JOIN services s ON s.id = p.service_id WHERE p.id = ?`, id).Scan(
		&value.kind, &value.gatewayID, &value.hostname, &value.protocol, &value.status, &enabled, &value.siteID,
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

func (s *Store) applyPublicationTLS(ctx context.Context, id string, enabled bool) (PublicationView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PublicationView{}, err
	}
	defer tx.Rollback()

	var current publicationTLSState
	var currentEnabled int
	if err := tx.QueryRowContext(ctx, `SELECT p.kind, COALESCE(p.gateway_node_id, ''), p.hostname, s.protocol, p.status, p.tls_enabled, s.site_id
		FROM publications p JOIN services s ON s.id = p.service_id WHERE p.id = ?`, id).Scan(
		&current.kind, &current.gatewayID, &current.hostname, &current.protocol, &current.status, &currentEnabled, &current.siteID,
	); errors.Is(err, sql.ErrNoRows) {
		return PublicationView{}, errors.New("center: publication not found")
	} else if err != nil {
		return PublicationView{}, err
	}
	current.enabled = currentEnabled == 1
	if err := validatePublicationTLSState(current); err != nil {
		return PublicationView{}, err
	}
	if enabled == current.enabled {
		_ = tx.Rollback()
		return s.Publication(ctx, id)
	}

	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE publications SET tls_enabled = ?, desired_revision = desired_revision + 1, status = 'pending', last_error = '', updated_at = ? WHERE id = ?`, enabled, now.Format(time.RFC3339Nano), id); err != nil {
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
	if err := tx.Commit(); err != nil {
		return PublicationView{}, err
	}
	return s.Publication(ctx, id)
}
