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

type CloudflareInput struct {
	AccountID string `json:"accountId"`
	ZoneID    string `json:"zoneId"`
	APIToken  string `json:"apiToken"`
}

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

type cloudflareEnvelope struct {
	Success bool              `json:"success"`
	Errors  []cloudflareError `json:"errors"`
	Result  json.RawMessage   `json:"result"`
}

func (s *Store) ConfigureCloudflare(ctx context.Context, input CloudflareInput) (IntegrationView, error) {
	input.AccountID = strings.TrimSpace(input.AccountID)
	input.ZoneID = strings.TrimSpace(input.ZoneID)
	input.APIToken = strings.TrimSpace(input.APIToken)
	existingSecretID, existingToken, err := s.integrationSecret(ctx, "cloudflare")
	if err != nil {
		return IntegrationView{}, err
	}
	replacingSecret := input.APIToken != ""
	if !replacingSecret {
		input.APIToken = existingToken
	}
	if input.AccountID == "" || input.ZoneID == "" || len(input.APIToken) < 20 {
		return IntegrationView{}, errors.New("center: Cloudflare Account, Zone, and API Token are required")
	}
	client := cloudflareClient{accountID: input.AccountID, zoneID: input.ZoneID, token: input.APIToken, baseURL: cloudflareAPIURL, http: &http.Client{Timeout: 20 * time.Second}}
	zoneName, err := client.verify(ctx)
	if err != nil {
		return IntegrationView{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IntegrationView{}, err
	}
	defer tx.Rollback()
	secretID := existingSecretID
	if replacingSecret {
		secretID, err = s.putSecret(ctx, tx, []byte(input.APIToken), "integration:cloudflare")
		if err != nil {
			return IntegrationView{}, err
		}
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO network_integrations(kind, mode, endpoint, account_id, zone_id, secret_id, status, created_at, updated_at)
		VALUES('cloudflare', 'managed', ?, ?, ?, ?, 'configured', ?, ?)
		ON CONFLICT(kind) DO UPDATE SET mode = 'managed', endpoint = excluded.endpoint, account_id = excluded.account_id, zone_id = excluded.zone_id, secret_id = excluded.secret_id, status = 'configured', last_error = '', updated_at = excluded.updated_at`, zoneName, input.AccountID, input.ZoneID, secretID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return IntegrationView{}, fmt.Errorf("center: save Cloudflare integration: %w", err)
	}
	if replacingSecret && existingSecretID != "" && existingSecretID != secretID {
		if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, existingSecretID); err != nil {
			return IntegrationView{}, fmt.Errorf("center: replace Cloudflare API token: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return IntegrationView{}, err
	}
	return s.Integration(ctx, "cloudflare")
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
	var accountID, zoneID, secretID string
	if err := s.db.QueryRowContext(ctx, `SELECT account_id, zone_id, secret_id FROM network_integrations WHERE kind = 'cloudflare' AND status = 'configured'`).Scan(&accountID, &zoneID, &secretID); errors.Is(err, sql.ErrNoRows) {
		return cloudflareClient{}, errors.New("center: Cloudflare integration is not configured")
	} else if err != nil {
		return cloudflareClient{}, err
	}
	token, err := s.getSecret(ctx, secretID, "integration:cloudflare")
	if err != nil {
		return cloudflareClient{}, err
	}
	return cloudflareClient{accountID: accountID, zoneID: zoneID, token: string(token), baseURL: cloudflareAPIURL, http: &http.Client{Timeout: 20 * time.Second}}, nil
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

func (s *Store) reconcileCloudflarePublication(ctx context.Context, publicationID string) error {
	var kind, gatewayID, hostname, dnsRecordID string
	if err := s.db.QueryRowContext(ctx, `SELECT kind, COALESCE(gateway_node_id, ''), hostname, dns_record_id FROM publications WHERE id = ? AND status <> 'stopped'`, publicationID).Scan(&kind, &gatewayID, &hostname, &dnsRecordID); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: publication not found")
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
			dnsRecordID, err = client.createDNSRecord(ctx, "CNAME", hostname, tunnelID+".cfargotunnel.com", true)
			if err != nil {
				return err
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE publications SET dns_record_id = ?, updated_at = ? WHERE id = ?`, dnsRecordID, s.now().UTC().Format(time.RFC3339Nano), publicationID); err != nil {
				return err
			}
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := s.queueTunnelState(ctx, tx, gatewayID, s.now().UTC()); err != nil {
			return err
		}
		return tx.Commit()
	}
	if kind == publicationPublic {
		var publicAddress string
		if err := s.db.QueryRowContext(ctx, `SELECT public_address FROM agent_network_profiles WHERE agent_id = ? AND direct_public = 1`, gatewayID).Scan(&publicAddress); err != nil {
			return errors.New("center: public entry node has no confirmed public address")
		}
		ip := net.ParseIP(publicAddress)
		if ip == nil {
			return errors.New("center: public entry address is invalid")
		}
		recordType := "AAAA"
		if ip.To4() != nil {
			recordType = "A"
		}
		if dnsRecordID == "" {
			dnsRecordID, err = client.createDNSRecord(ctx, recordType, hostname, ip.String(), false)
			if err != nil {
				return err
			}
			_, err = s.db.ExecContext(ctx, `UPDATE publications SET dns_record_id = ?, updated_at = ? WHERE id = ?`, dnsRecordID, s.now().UTC().Format(time.RFC3339Nano), publicationID)
		}
		return err
	}
	return nil
}

func (s *Store) removeCloudflarePublication(ctx context.Context, id, kind, gatewayID, dnsRecordID string) error {
	client, err := s.cloudflare(ctx)
	if err != nil {
		return err
	}
	if dnsRecordID != "" {
		if err := client.deleteDNSRecord(ctx, dnsRecordID); err != nil {
			return err
		}
	}
	if kind != publicationCloudflare {
		return nil
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.queueTunnelState(ctx, tx, gatewayID, s.now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
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
