package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
)

var cloudflareAccessScopes = []string{"access.write", "access-acct.write"}

type CenterRemoteAccessInput struct {
	Enabled       bool   `json:"enabled"`
	AudienceKind  string `json:"audienceKind,omitempty"`
	AudienceValue string `json:"audienceValue,omitempty"`
}

type CenterRemoteAccessView struct {
	Available     bool      `json:"available"`
	Enabled       bool      `json:"enabled"`
	Hostname      string    `json:"hostname,omitempty"`
	AudienceKind  string    `json:"audienceKind,omitempty"`
	AudienceValue string    `json:"audienceValue,omitempty"`
	Status        string    `json:"status"`
	LastError     string    `json:"lastError,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt,omitempty"`
}

type centerRemoteAccessRecord struct {
	CenterRemoteAccessView
	IdentityProviderID string
	ApplicationID      string
	TunnelID           string
	TunnelSecretID     string
	DNSRecordID        string
}

func (s *Store) CenterRemoteAccess(ctx context.Context, available bool) (CenterRemoteAccessView, error) {
	record, exists, err := s.centerRemoteAccessRecord(ctx)
	if err != nil {
		return CenterRemoteAccessView{}, err
	}
	if !exists {
		return CenterRemoteAccessView{Available: available, Status: "disabled"}, nil
	}
	record.Available = available
	record.Enabled = record.Status == "configured"
	return record.CenterRemoteAccessView, nil
}

func (s *Store) centerRemoteAccessRecord(ctx context.Context) (centerRemoteAccessRecord, bool, error) {
	var record centerRemoteAccessRecord
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT hostname, audience_kind, audience_value, otp_identity_provider_id,
		access_application_id, tunnel_id, COALESCE(tunnel_token_secret_id, ''), dns_record_id, status, last_error, updated_at
		FROM center_remote_access WHERE id = 1`).Scan(
		&record.Hostname, &record.AudienceKind, &record.AudienceValue, &record.IdentityProviderID,
		&record.ApplicationID, &record.TunnelID, &record.TunnelSecretID, &record.DNSRecordID,
		&record.Status, &record.LastError, &updated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return centerRemoteAccessRecord{}, false, nil
	}
	if err != nil {
		return centerRemoteAccessRecord{}, false, fmt.Errorf("center: read remote access configuration: %w", err)
	}
	record.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return record, true, nil
}

func normalizeCenterRemoteAccess(input CenterRemoteAccessInput, centerURL, zoneName string) (CenterRemoteAccessInput, string, error) {
	if !input.Enabled {
		return CenterRemoteAccessInput{}, "", nil
	}
	parsed, err := url.Parse(strings.TrimSpace(centerURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Port() != "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return CenterRemoteAccessInput{}, "", errors.New("center: remote access requires a standard HTTPS Center hostname")
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	zoneName = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zoneName), "."))
	if hostname != zoneName && !strings.HasSuffix(hostname, "."+zoneName) {
		return CenterRemoteAccessInput{}, "", fmt.Errorf("center: %s is outside the selected Cloudflare zone %s", hostname, zoneName)
	}
	input.AudienceKind = strings.TrimSpace(input.AudienceKind)
	input.AudienceValue = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(input.AudienceValue, "@")))
	switch input.AudienceKind {
	case "email":
		address, parseErr := mail.ParseAddress(input.AudienceValue)
		if parseErr != nil || address.Address != input.AudienceValue || strings.Count(input.AudienceValue, "@") != 1 {
			return CenterRemoteAccessInput{}, "", errors.New("center: enter one valid email address for Cloudflare Access")
		}
	case "email_domain":
		if !domainSuffixPattern.MatchString(input.AudienceValue) {
			return CenterRemoteAccessInput{}, "", errors.New("center: enter a valid email domain for Cloudflare Access")
		}
	default:
		return CenterRemoteAccessInput{}, "", errors.New("center: Cloudflare Access audience must be email or email_domain")
	}
	return input, hostname, nil
}

func (s *Server) ConfigureCenterRemoteAccess(ctx context.Context, input CenterRemoteAccessInput, centerURL string) (CenterRemoteAccessView, error) {
	s.store.domainSwitchMu.Lock()
	defer s.store.domainSwitchMu.Unlock()
	s.store.remoteAccessMu.Lock()
	defer s.store.remoteAccessMu.Unlock()
	if !input.Enabled {
		_, exists, err := s.store.centerRemoteAccessRecord(ctx)
		if err != nil {
			return CenterRemoteAccessView{}, err
		}
		if !exists {
			return s.store.CenterRemoteAccess(ctx, s.infrastructure != nil)
		}
		if s.infrastructure == nil {
			return CenterRemoteAccessView{}, errors.New("center: this installation does not include the remote access deployment helper")
		}
		if err := s.disableCenterRemoteAccess(ctx); err != nil {
			view, viewErr := s.store.CenterRemoteAccess(ctx, s.infrastructure != nil)
			return view, errors.Join(err, viewErr)
		}
		return s.store.CenterRemoteAccess(ctx, s.infrastructure != nil)
	}
	if s.infrastructure == nil {
		return CenterRemoteAccessView{}, errors.New("center: this installation does not include the remote access deployment helper")
	}
	client, err := s.store.cloudflareWithScopes(ctx, cloudflareAccessScopes...)
	if err != nil {
		return CenterRemoteAccessView{}, err
	}
	zoneName, err := client.verify(ctx)
	if err != nil {
		return CenterRemoteAccessView{}, err
	}
	normalized, hostname, err := normalizeCenterRemoteAccess(input, centerURL, zoneName)
	if err != nil {
		return CenterRemoteAccessView{}, err
	}
	current, exists, err := s.store.centerRemoteAccessRecord(ctx)
	if err != nil {
		return CenterRemoteAccessView{}, err
	}
	if exists && current.Status == "configured" && current.Hostname == hostname && current.AudienceKind == normalized.AudienceKind && current.AudienceValue == normalized.AudienceValue {
		return s.store.CenterRemoteAccess(ctx, s.infrastructure != nil)
	}
	if exists {
		if err := s.disableCenterRemoteAccess(ctx); err != nil {
			return CenterRemoteAccessView{}, fmt.Errorf("center: remove the previous remote access configuration: %w", err)
		}
	}
	now := s.store.now().UTC().Format(time.RFC3339Nano)
	if _, err := s.store.db.ExecContext(ctx, `INSERT INTO center_remote_access(id, hostname, audience_kind, audience_value, status, created_at, updated_at)
		VALUES(1, ?, ?, ?, 'pending', ?, ?)`, hostname, normalized.AudienceKind, normalized.AudienceValue, now, now); err != nil {
		return CenterRemoteAccessView{}, fmt.Errorf("center: start remote access configuration: %w", err)
	}
	if err := s.applyCenterRemoteAccess(ctx, client); err != nil {
		cleanupErr := s.cleanupCenterRemoteAccess(context.WithoutCancel(ctx), client)
		failure := errors.Join(err, cleanupErr)
		message := failure.Error()
		_, statusErr := s.store.db.ExecContext(context.WithoutCancel(ctx), `UPDATE center_remote_access SET status = 'failed', last_error = ?, updated_at = ? WHERE id = 1`, message, s.store.now().UTC().Format(time.RFC3339Nano))
		return CenterRemoteAccessView{}, errors.Join(failure, statusErr)
	}
	return s.store.CenterRemoteAccess(ctx, s.infrastructure != nil)
}

func (s *Server) applyCenterRemoteAccess(ctx context.Context, client cloudflareClient) error {
	record, exists, err := s.store.centerRemoteAccessRecord(ctx)
	if err != nil || !exists {
		return errors.Join(errors.New("center: remote access configuration is missing"), err)
	}
	if err := client.ensureAccessOrganization(ctx); err != nil {
		return err
	}
	identityProviderID, err := client.ensureOneTimePINIdentityProvider(ctx)
	if err != nil {
		return err
	}
	if err := s.store.updateCenterRemoteAccessResource(ctx, "otp_identity_provider_id", identityProviderID); err != nil {
		return err
	}
	applicationID, err := client.createAccessApplication(ctx, record.Hostname, record.AudienceKind, record.AudienceValue, identityProviderID)
	if err != nil {
		return err
	}
	if err := s.store.updateCenterRemoteAccessResource(ctx, "access_application_id", applicationID); err != nil {
		return errors.Join(err, client.deleteAccessApplication(context.WithoutCancel(ctx), applicationID))
	}
	tunnelID, err := client.createTunnel(ctx, "vastora-center")
	if err != nil {
		return err
	}
	if err := s.store.updateCenterRemoteAccessResource(ctx, "tunnel_id", tunnelID); err != nil {
		return errors.Join(err, client.deleteTunnel(context.WithoutCancel(ctx), tunnelID))
	}
	token, err := client.tunnelToken(ctx, tunnelID)
	if err != nil {
		return err
	}
	if err := s.store.saveCenterRemoteAccessTunnelToken(ctx, token); err != nil {
		return err
	}
	if err := client.putTunnelConfiguration(ctx, tunnelID, []TunnelTaskIngress{{Hostname: record.Hostname, Service: "http://vastora-center:8080"}}); err != nil {
		return fmt.Errorf("center: configure Center Tunnel ingress: %w", err)
	}
	if err := s.infrastructure.ApplyCenterRemoteAccess(ctx, deployapi.CenterRemoteAccessRequest{Enabled: true, Token: token}); err != nil {
		return err
	}
	existing, err := client.listDNSRecords(ctx, record.Hostname)
	if err != nil {
		return err
	}
	if len(existing) != 0 {
		return fmt.Errorf("center: DNS record %s already exists; Vastora did not overwrite it", record.Hostname)
	}
	dnsRecordID, err := client.createDNSRecord(ctx, "CNAME", record.Hostname, tunnelID+".cfargotunnel.com", true)
	if err != nil {
		return err
	}
	if err := s.store.updateCenterRemoteAccessResource(ctx, "dns_record_id", dnsRecordID); err != nil {
		return errors.Join(err, client.deleteDNSRecord(context.WithoutCancel(ctx), dnsRecordID))
	}
	_, err = s.store.db.ExecContext(ctx, `UPDATE center_remote_access SET status = 'configured', last_error = '', updated_at = ? WHERE id = 1`, s.store.now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) updateCenterRemoteAccessResource(ctx context.Context, column, value string) error {
	allowed := map[string]bool{"otp_identity_provider_id": true, "access_application_id": true, "tunnel_id": true, "dns_record_id": true}
	if !allowed[column] {
		return errors.New("center: invalid remote access resource field")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE center_remote_access SET `+column+` = ?, updated_at = ? WHERE id = 1`, value, s.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("center: remote access configuration changed while applying")
	}
	return nil
}

func (s *Store) saveCenterRemoteAccessTunnelToken(ctx context.Context, token string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	secretID, err := s.putSecret(ctx, tx, []byte(token), "center-remote-access-tunnel")
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE center_remote_access SET tunnel_token_secret_id = ?, updated_at = ? WHERE id = 1`, secretID, s.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("center: remote access configuration changed while storing its Tunnel token")
	}
	return tx.Commit()
}

func (s *Server) disableCenterRemoteAccess(ctx context.Context) error {
	record, exists, err := s.store.centerRemoteAccessRecord(ctx)
	if err != nil || !exists {
		return err
	}
	client, clientErr := s.store.cloudflareWithScopes(ctx, cloudflareAccessScopes...)
	if clientErr != nil {
		stopErr := s.infrastructure.ApplyCenterRemoteAccess(context.WithoutCancel(ctx), deployapi.CenterRemoteAccessRequest{Enabled: false})
		failure := errors.Join(clientErr, stopErr)
		_, statusErr := s.store.db.ExecContext(context.WithoutCancel(ctx), `UPDATE center_remote_access SET status = 'failed', last_error = ?, updated_at = ? WHERE id = 1`, failure.Error(), s.store.now().UTC().Format(time.RFC3339Nano))
		return errors.Join(failure, statusErr)
	}
	if err := s.cleanupCenterRemoteAccess(ctx, client); err != nil {
		_, _ = s.store.db.ExecContext(context.WithoutCancel(ctx), `UPDATE center_remote_access SET status = 'failed', last_error = ?, updated_at = ? WHERE id = 1`, err.Error(), s.store.now().UTC().Format(time.RFC3339Nano))
		return err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM center_remote_access WHERE id = 1`); err != nil {
		return err
	}
	if record.TunnelSecretID != "" {
		if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, record.TunnelSecretID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Server) cleanupCenterRemoteAccess(ctx context.Context, client cloudflareClient) error {
	record, exists, err := s.store.centerRemoteAccessRecord(ctx)
	if err != nil || !exists {
		return err
	}
	var cleanup []error
	if record.DNSRecordID != "" {
		if err := client.deleteDNSRecord(ctx, record.DNSRecordID); err != nil && !cloudflareResourceNotFound(err) {
			cleanup = append(cleanup, fmt.Errorf("remove Center remote DNS: %w", err))
		} else if err := s.store.updateCenterRemoteAccessResource(ctx, "dns_record_id", ""); err != nil {
			cleanup = append(cleanup, fmt.Errorf("record removed Center remote DNS: %w", err))
		}
	}
	if err := s.infrastructure.ApplyCenterRemoteAccess(ctx, deployapi.CenterRemoteAccessRequest{Enabled: false}); err != nil {
		cleanup = append(cleanup, err)
	}
	if record.ApplicationID != "" {
		if err := client.deleteAccessApplication(ctx, record.ApplicationID); err != nil && !cloudflareResourceNotFound(err) {
			cleanup = append(cleanup, fmt.Errorf("remove Center Access application: %w", err))
		} else if err := s.store.updateCenterRemoteAccessResource(ctx, "access_application_id", ""); err != nil {
			cleanup = append(cleanup, fmt.Errorf("record removed Center Access application: %w", err))
		}
	}
	if record.TunnelID != "" {
		if err := client.deleteTunnel(ctx, record.TunnelID); err != nil && !cloudflareResourceNotFound(err) {
			cleanup = append(cleanup, fmt.Errorf("remove Center Tunnel: %w", err))
		} else if err := s.store.updateCenterRemoteAccessResource(ctx, "tunnel_id", ""); err != nil {
			cleanup = append(cleanup, fmt.Errorf("record removed Center Tunnel: %w", err))
		}
	}
	return errors.Join(cleanup...)
}

func cloudflareResourceNotFound(err error) bool {
	var failure *cloudflareAPIError
	return errors.As(err, &failure) && failure.StatusCode == http.StatusNotFound
}
