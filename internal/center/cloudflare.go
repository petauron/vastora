package center

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const cloudflareAPIURL = "https://api.cloudflare.com/client/v4"

type IntegrationView struct {
	Kind      string    `json:"kind"`
	Mode      string    `json:"mode,omitempty"`
	Endpoint  string    `json:"endpoint,omitempty"`
	AccountID string    `json:"accountId,omitempty"`
	ZoneID    string    `json:"zoneId,omitempty"`
	SecretSet bool      `json:"secretSet"`
	Status    string    `json:"status"`
	LastError string    `json:"lastError,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type cloudflareClient struct {
	accountID string
	zoneID    string
	token     string
	baseURL   string
	http      *http.Client
}

type cloudflareError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

var errStalePublicationReconcile = errors.New("center: publication changed during external reconciliation")

type cloudflareEnvelope struct {
	Success bool              `json:"success"`
	Errors  []cloudflareError `json:"errors"`
	Result  json.RawMessage   `json:"result"`
}

func (s *Store) integrationSecret(ctx context.Context, kind string) (string, string, error) {
	var secretID string
	err := s.db.QueryRowContext(ctx, `SELECT secret_id FROM network_integrations WHERE kind = ? AND secret_id IS NOT NULL`, kind).Scan(&secretID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("center: read %s integration secret: %w", kind, err)
	}
	value, err := s.getSecret(ctx, secretID, "integration:"+kind)
	if err != nil {
		return "", "", err
	}
	return secretID, string(value), nil
}

func (s *Store) Integration(ctx context.Context, kind string) (IntegrationView, error) {
	var value IntegrationView
	var secretID sql.NullString
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT kind, mode, endpoint, account_id, zone_id, secret_id, status, last_error, updated_at FROM network_integrations WHERE kind = ?`, kind).Scan(&value.Kind, &value.Mode, &value.Endpoint, &value.AccountID, &value.ZoneID, &secretID, &value.Status, &value.LastError, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return IntegrationView{Kind: kind, Status: "disabled"}, nil
	}
	if err != nil {
		return IntegrationView{}, err
	}
	value.SecretSet = secretID.Valid
	value.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return value, nil
}

func (s *Store) ListIntegrations(ctx context.Context) ([]IntegrationView, error) {
	result := make([]IntegrationView, 0, 2)
	for _, kind := range []string{"headscale", "cloudflare"} {
		value, err := s.Integration(ctx, kind)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (s *Store) cloudflare(ctx context.Context) (cloudflareClient, error) {
	s.cloudflareTokenMu.Lock()
	defer s.cloudflareTokenMu.Unlock()
	var accountID, zoneID, secretID string
	var mode string
	if err := s.db.QueryRowContext(ctx, `SELECT mode, account_id, zone_id, secret_id FROM network_integrations WHERE kind = 'cloudflare' AND status = 'configured'`).Scan(&mode, &accountID, &zoneID, &secretID); errors.Is(err, sql.ErrNoRows) {
		return cloudflareClient{}, errors.New("center: Cloudflare integration is not configured")
	} else if err != nil {
		return cloudflareClient{}, err
	}
	if mode != "oauth" {
		return cloudflareClient{}, errors.New("center: reconnect Cloudflare with OAuth before using it")
	}
	encoded, err := s.getSecret(ctx, secretID, "integration:cloudflare")
	if err != nil {
		return cloudflareClient{}, err
	}
	var token cloudflareOAuthToken
	if err := json.Unmarshal(encoded, &token); err != nil || token.AccessToken == "" || token.RefreshToken == "" {
		return cloudflareClient{}, errors.New("center: reconnect Cloudflare with OAuth before using it")
	}
	if !s.now().UTC().Before(token.ExpiresAt.Add(-2 * time.Minute)) {
		token, err = s.refreshCloudflareToken(ctx, token.RefreshToken)
		if err != nil {
			return cloudflareClient{}, err
		}
		if err := s.saveCloudflareOAuthToken(ctx, secretID, token); err != nil {
			return cloudflareClient{}, err
		}
	}
	return cloudflareClient{accountID: accountID, zoneID: zoneID, token: token.AccessToken, baseURL: s.cloudflareOAuth.APIURL, http: s.cloudflareOAuth.HTTPClient}, nil
}

func (s *Store) CloudflareZones(ctx context.Context) ([]CloudflareZone, error) {
	client, err := s.cloudflare(ctx)
	if err != nil {
		return nil, err
	}
	zones, err := s.listCloudflareZones(ctx, client.token)
	if err != nil {
		return nil, err
	}
	if len(zones) == 0 {
		return nil, errors.New("center: Cloudflare authorization has no accessible zones")
	}
	return zones, nil
}

func (s *Store) ensureCloudflareTunnel(ctx context.Context, agentID string) error {
	var existing int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cloudflare_tunnels WHERE agent_id = ?`, agentID).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return nil
	}
	client, err := s.cloudflare(ctx)
	if err != nil {
		return err
	}
	var nodeName string
	if err := s.db.QueryRowContext(ctx, `SELECT name FROM agents WHERE id = ?`, agentID).Scan(&nodeName); err != nil {
		return errors.New("center: Tunnel node not found")
	}
	tunnelName := "vastora-" + sanitizeCloudflareName(nodeName) + "-" + agentID[:min(8, len(agentID))]
	tunnelID, err := client.createTunnel(ctx, tunnelName)
	if err != nil {
		return err
	}
	token, err := client.tunnelToken(ctx, tunnelID)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	secretID, err := s.putSecret(ctx, tx, []byte(token), "cloudflare-tunnel:"+agentID)
	if err != nil {
		return err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO cloudflare_tunnels(agent_id, tunnel_id, tunnel_name, token_secret_id, desired_json, status, created_at, updated_at) VALUES(?, ?, ?, ?, '{}', 'stopped', ?, ?)`, agentID, tunnelID, tunnelName, secretID, now, now); err != nil {
		return fmt.Errorf("center: save Cloudflare Tunnel: %w", err)
	}
	return tx.Commit()
}

func (s *Store) reconcileCloudflarePublication(ctx context.Context, publicationID string, revision int64) error {
	var kind, gatewayID, hostname, dnsRecordID string
	if err := s.db.QueryRowContext(ctx, `SELECT kind, COALESCE(gateway_node_id, ''), hostname, dns_record_id FROM publications WHERE id = ? AND desired_revision = ? AND status <> 'stopped'`, publicationID, revision).Scan(&kind, &gatewayID, &hostname, &dnsRecordID); errors.Is(err, sql.ErrNoRows) {
		return errStalePublicationReconcile
	} else if err != nil {
		return err
	}
	client, err := s.cloudflare(ctx)
	if err != nil {
		return err
	}
	if kind == publicationCloudflare {
		if err := s.ensureCloudflareTunnel(ctx, gatewayID); err != nil {
			return err
		}
		var tunnelID string
		if err := s.db.QueryRowContext(ctx, `SELECT tunnel_id FROM cloudflare_tunnels WHERE agent_id = ?`, gatewayID).Scan(&tunnelID); err != nil {
			return err
		}
		ingress, err := s.cloudflareIngress(ctx, gatewayID)
		if err != nil {
			return err
		}
		if err := client.putTunnelConfiguration(ctx, tunnelID, ingress); err != nil {
			return err
		}
		if dnsRecordID == "" {
			createdRecordID, createErr := client.createDNSRecord(ctx, "CNAME", hostname, tunnelID+".cfargotunnel.com", true)
			err = createErr
			if err != nil {
				return err
			}
			updated, updateErr := s.db.ExecContext(ctx, `UPDATE publications SET dns_record_id = ?, updated_at = ? WHERE id = ? AND desired_revision = ? AND status <> 'stopped' AND dns_record_id = ''`, createdRecordID, s.now().UTC().Format(time.RFC3339Nano), publicationID, revision)
			if updateErr != nil {
				return errors.Join(updateErr, s.compensateUntrackedCloudflareDNS(ctx, client, publicationID, createdRecordID))
			}
			if changed, _ := updated.RowsAffected(); changed != 1 {
				if cleanupErr := s.compensateUntrackedCloudflareDNS(ctx, client, publicationID, createdRecordID); cleanupErr != nil {
					return cleanupErr
				}
				return errStalePublicationReconcile
			}
			dnsRecordID = createdRecordID
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM publications WHERE id = ? AND desired_revision = ? AND status <> 'stopped'`, publicationID, revision).Scan(&active); errors.Is(err, sql.ErrNoRows) {
			return errStalePublicationReconcile
		} else if err != nil {
			return err
		}
		if err := s.queueTunnelState(ctx, tx, gatewayID, s.now().UTC()); err != nil {
			return err
		}
		return tx.Commit()
	}
	if kind == publicationPublic || kind == publicationShared443 {
		var publicAddress string
		if err := s.db.QueryRowContext(ctx, `SELECT public_address FROM agent_network_profiles WHERE agent_id = ? AND direct_public = 1`, gatewayID).Scan(&publicAddress); err != nil {
			return errors.New("center: public entry node has no confirmed public address")
		}
		ip := net.ParseIP(publicAddress)
		if ip == nil || ip.To4() == nil {
			return errors.New("center: public entry address must be IPv4")
		}
		recordType := "A"
		if dnsRecordID == "" {
			createdRecordID, createErr := client.createDNSRecord(ctx, recordType, hostname, ip.String(), false)
			if createErr != nil {
				return createErr
			}
			updated, updateErr := s.db.ExecContext(ctx, `UPDATE publications SET dns_record_id = ?, updated_at = ? WHERE id = ? AND desired_revision = ? AND status <> 'stopped' AND dns_record_id = ''`, createdRecordID, s.now().UTC().Format(time.RFC3339Nano), publicationID, revision)
			if updateErr != nil {
				return errors.Join(updateErr, s.compensateUntrackedCloudflareDNS(ctx, client, publicationID, createdRecordID))
			}
			if changed, _ := updated.RowsAffected(); changed != 1 {
				if cleanupErr := s.compensateUntrackedCloudflareDNS(ctx, client, publicationID, createdRecordID); cleanupErr != nil {
					return cleanupErr
				}
				return errStalePublicationReconcile
			}
		}
		return nil
	}
	return nil
}

func (s *Store) compensateUntrackedCloudflareDNS(ctx context.Context, client cloudflareClient, publicationID, recordID string) error {
	if err := client.deleteDNSRecord(ctx, recordID); err != nil {
		_, saveErr := s.db.ExecContext(ctx, `UPDATE publications SET dns_record_id = ?, cleanup_pending = CASE WHEN status = 'stopped' THEN 1 ELSE cleanup_pending END, updated_at = ? WHERE id = ? AND dns_record_id = ''`, recordID, s.now().UTC().Format(time.RFC3339Nano), publicationID)
		return errors.Join(fmt.Errorf("center: remove untracked Cloudflare DNS record: %w", err), saveErr)
	}
	return nil
}

func (s *Store) removeCloudflarePublication(ctx context.Context, id, kind, gatewayID, dnsRecordID string) error {
	client, err := s.cloudflare(ctx)
	cleanupErrors := []error{}
	if err != nil {
		cleanupErrors = append(cleanupErrors, err)
	} else if dnsRecordID != "" {
		if err := client.deleteDNSRecord(ctx, dnsRecordID); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		} else if _, err := s.db.ExecContext(ctx, `UPDATE publications SET dns_record_id = '', updated_at = ? WHERE id = ?`, s.now().UTC().Format(time.RFC3339Nano), id); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if kind != publicationCloudflare {
		return errors.Join(cleanupErrors...)
	}
	if err == nil {
		var tunnelID string
		if queryErr := s.db.QueryRowContext(ctx, `SELECT tunnel_id FROM cloudflare_tunnels WHERE agent_id = ?`, gatewayID).Scan(&tunnelID); queryErr != nil {
			cleanupErrors = append(cleanupErrors, queryErr)
		} else if ingress, ingressErr := s.cloudflareIngress(ctx, gatewayID); ingressErr != nil {
			cleanupErrors = append(cleanupErrors, ingressErr)
		} else if updateErr := client.putTunnelConfiguration(ctx, tunnelID, ingress); updateErr != nil {
			cleanupErrors = append(cleanupErrors, updateErr)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		cleanupErrors = append(cleanupErrors, err)
		return errors.Join(cleanupErrors...)
	}
	defer tx.Rollback()
	if err := s.queueTunnelState(ctx, tx, gatewayID, s.now().UTC()); err != nil {
		cleanupErrors = append(cleanupErrors, err)
		return errors.Join(cleanupErrors...)
	}
	if err := tx.Commit(); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	return errors.Join(cleanupErrors...)
}

func (s *Store) cloudflareIngress(ctx context.Context, agentID string) ([]TunnelTaskIngress, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.hostname, s.protocol, s.endpoint FROM publications p JOIN services s ON s.id = p.service_id WHERE p.gateway_node_id = ? AND p.kind = 'cloudflare_tunnel' AND p.status <> 'stopped' AND s.status <> 'stopped' ORDER BY p.hostname, p.id`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []TunnelTaskIngress{}
	for rows.Next() {
		var value TunnelTaskIngress
		var protocol, endpoint string
		if err := rows.Scan(&value.Hostname, &protocol, &endpoint); err != nil {
			return nil, err
		}
		value.Service = protocol + "://" + endpoint
		values = append(values, value)
	}
	return values, rows.Err()
}

func (client cloudflareClient) verify(ctx context.Context) (string, error) {
	var ignored json.RawMessage
	if err := client.do(ctx, http.MethodGet, "/accounts/"+url.PathEscape(client.accountID)+"/cfd_tunnel?per_page=1", nil, &ignored); err != nil {
		return "", fmt.Errorf("center: verify Cloudflare Tunnel permission: %w", err)
	}
	var zone struct {
		Name string `json:"name"`
	}
	if err := client.do(ctx, http.MethodGet, "/zones/"+url.PathEscape(client.zoneID), nil, &zone); err != nil {
		return "", fmt.Errorf("center: verify Cloudflare Zone permission: %w", err)
	}
	zone.Name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zone.Name), "."))
	if !domainSuffixPattern.MatchString(zone.Name) {
		return "", errors.New("center: Cloudflare returned an invalid Zone name")
	}
	return zone.Name, nil
}

func (client cloudflareClient) createTunnel(ctx context.Context, name string) (string, error) {
	var result struct {
		ID string `json:"id"`
	}
	err := client.do(ctx, http.MethodPost, "/accounts/"+url.PathEscape(client.accountID)+"/cfd_tunnel", map[string]any{"name": name, "config_src": "cloudflare"}, &result)
	if err != nil {
		return "", fmt.Errorf("center: create Cloudflare Tunnel: %w", err)
	}
	if result.ID == "" {
		return "", errors.New("center: Cloudflare did not return a Tunnel ID")
	}
	return result.ID, nil
}

func (client cloudflareClient) tunnelToken(ctx context.Context, tunnelID string) (string, error) {
	var token string
	err := client.do(ctx, http.MethodGet, "/accounts/"+url.PathEscape(client.accountID)+"/cfd_tunnel/"+url.PathEscape(tunnelID)+"/token", nil, &token)
	if err != nil {
		return "", fmt.Errorf("center: get Cloudflare Tunnel token: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return "", errors.New("center: Cloudflare did not return a Tunnel token")
	}
	return token, nil
}

func (client cloudflareClient) putTunnelConfiguration(ctx context.Context, tunnelID string, ingress []TunnelTaskIngress) error {
	rules := make([]map[string]string, 0, len(ingress)+1)
	for _, value := range ingress {
		rules = append(rules, map[string]string{"hostname": value.Hostname, "service": value.Service})
	}
	rules = append(rules, map[string]string{"service": "http_status:404"})
	var ignored json.RawMessage
	return client.do(ctx, http.MethodPut, "/accounts/"+url.PathEscape(client.accountID)+"/cfd_tunnel/"+url.PathEscape(tunnelID)+"/configurations", map[string]any{"config": map[string]any{"ingress": rules}}, &ignored)
}

func (client cloudflareClient) createDNSRecord(ctx context.Context, recordType, name, content string, proxied bool) (string, error) {
	var result struct {
		ID string `json:"id"`
	}
	err := client.do(ctx, http.MethodPost, "/zones/"+url.PathEscape(client.zoneID)+"/dns_records", map[string]any{"type": recordType, "name": name, "content": content, "proxied": proxied}, &result)
	if err != nil {
		return "", fmt.Errorf("center: create Cloudflare DNS record: %w", err)
	}
	if result.ID == "" {
		return "", errors.New("center: Cloudflare did not return a DNS record ID")
	}
	return result.ID, nil
}

func (client cloudflareClient) deleteDNSRecord(ctx context.Context, recordID string) error {
	var ignored json.RawMessage
	return client.do(ctx, http.MethodDelete, "/zones/"+url.PathEscape(client.zoneID)+"/dns_records/"+url.PathEscape(recordID), nil, &ignored)
}

func (client cloudflareClient) do(ctx context.Context, method, path string, body any, output any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(client.baseURL, "/")+path, payload)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return err
	}
	var envelope cloudflareEnvelope
	if json.Unmarshal(raw, &envelope) != nil {
		return fmt.Errorf("cloudflare returned HTTP %d", response.StatusCode)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.Success {
		message := response.Status
		if len(envelope.Errors) != 0 {
			message = envelope.Errors[0].Message
		}
		return errors.New(message)
	}
	if output != nil && len(envelope.Result) != 0 && string(envelope.Result) != "null" {
		if err := json.Unmarshal(envelope.Result, output); err != nil {
			return err
		}
	}
	return nil
}

func sanitizeCloudflareName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
			builder.WriteRune(character)
		} else if builder.Len() != 0 {
			builder.WriteByte('-')
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "node"
	}
	return result
}
