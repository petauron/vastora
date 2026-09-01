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

	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/gatewayruntime"
)

const cloudflareAPIURL = "https://api.cloudflare.com/client/v4"

type IntegrationView struct {
	Kind                string     `json:"kind"`
	Mode                string     `json:"mode,omitempty"`
	Endpoint            string     `json:"endpoint,omitempty"`
	AccountID           string     `json:"accountId,omitempty"`
	ZoneID              string     `json:"zoneId,omitempty"`
	SecretSet           bool       `json:"secretSet"`
	AccessManagement    bool       `json:"accessManagement,omitempty"`
	Status              string     `json:"status"`
	LastError           string     `json:"lastError,omitempty"`
	UpdatedAt           time.Time  `json:"updatedAt"`
	CredentialStatus    string     `json:"credentialStatus,omitempty"`
	CredentialExpiresAt *time.Time `json:"credentialExpiresAt,omitempty"`
	DNSPolicy           string     `json:"dnsPolicy,omitempty"`
	DNSResolvers        []string   `json:"dnsResolvers,omitempty"`
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

type cloudflareAPIError struct {
	StatusCode int
	Code       int
	Message    string
}

func (failure *cloudflareAPIError) Error() string {
	return failure.Message
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
	if kind == "cloudflare" && value.Mode == "oauth" && secretID.Valid {
		if encoded, secretErr := s.getSecret(ctx, secretID.String, "integration:cloudflare"); secretErr == nil {
			var token cloudflareOAuthToken
			if json.Unmarshal(encoded, &token) == nil {
				value.AccessManagement = oauthScopesGranted(token.Scope, cloudflareAccessScopes...)
			}
		}
	}
	if kind == "headscale" && value.Mode == "builtin" {
		value.DNSPolicy, value.DNSResolvers, err = s.builtinHeadscaleDNSConfig(ctx)
		if err != nil {
			return IntegrationView{}, err
		}
		if key, exists, keyErr := s.headscaleAPIKeyState(ctx); keyErr != nil {
			return IntegrationView{}, keyErr
		} else if exists {
			value.CredentialStatus = key.State
			if !key.ExpiresAt.IsZero() {
				expiresAt := key.ExpiresAt
				value.CredentialExpiresAt = &expiresAt
			}
			if key.LastError != "" {
				value.LastError = key.LastError
			}
		}
	}
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
	return s.cloudflareWithScopes(ctx)
}

func (s *Store) cloudflareWithScopes(ctx context.Context, requiredScopes ...string) (cloudflareClient, error) {
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
		token, err = s.refreshCloudflareToken(ctx, token)
		if err != nil {
			return cloudflareClient{}, err
		}
		if err := s.saveCloudflareOAuthToken(ctx, secretID, token); err != nil {
			return cloudflareClient{}, err
		}
	}
	if !oauthScopesGranted(token.Scope, requiredScopes...) {
		return cloudflareClient{}, fmt.Errorf("center: reconnect Cloudflare to grant %s permission", strings.Join(requiredScopes, " and "))
	}
	return cloudflareClient{accountID: accountID, zoneID: zoneID, token: token.AccessToken, baseURL: s.cloudflareOAuth.APIURL, http: s.cloudflareOAuth.HTTPClient}, nil
}

func oauthScopeGranted(scopes, required string) bool {
	for _, scope := range strings.FieldsFunc(scopes, func(value rune) bool { return value == ' ' || value == ',' }) {
		if scope == required {
			return true
		}
	}
	return false
}

func oauthScopesGranted(scopes string, required ...string) bool {
	for _, scope := range required {
		if !oauthScopeGranted(scopes, scope) {
			return false
		}
	}
	return true
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

func (s *Store) reconcileCloudflarePublication(ctx context.Context, publicationID string, revision int64) error {
	var kind, gatewayID, hostname, dnsRecordID, accessApplicationID, appKey, serviceName string
	if err := s.db.QueryRowContext(ctx, `SELECT p.kind, COALESCE(p.gateway_node_id, ''), p.hostname, p.dns_record_id, p.access_application_id, a.app_key, s.name
		FROM publications p JOIN services s ON s.id = p.service_id JOIN applications a ON a.id = s.application_id
		WHERE p.id = ? AND p.desired_revision = ? AND p.status <> 'stopped'`, publicationID, revision).Scan(&kind, &gatewayID, &hostname, &dnsRecordID, &accessApplicationID, &appKey, &serviceName); errors.Is(err, sql.ErrNoRows) {
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
			dnsRecordID, err = s.reusePublicationDNSRecord(ctx, publicationID, kind, gatewayID, hostname)
			if err != nil {
				return err
			}
		}
		if dnsRecordID == "" {
			createdRecordID, created, createErr := client.ensureDNSRecord(ctx, "CNAME", hostname, tunnelID+".cfargotunnel.com", true)
			err = createErr
			if err != nil {
				return err
			}
			updated, updateErr := s.db.ExecContext(ctx, `UPDATE publications SET dns_record_id = ?, updated_at = ? WHERE id = ? AND desired_revision = ? AND status <> 'stopped' AND dns_record_id = ''`, createdRecordID, s.now().UTC().Format(time.RFC3339Nano), publicationID, revision)
			if updateErr != nil {
				if created {
					return errors.Join(updateErr, s.compensateUntrackedCloudflareDNS(ctx, client, publicationID, createdRecordID))
				}
				return updateErr
			}
			if changed, _ := updated.RowsAffected(); changed != 1 {
				if created {
					if cleanupErr := s.compensateUntrackedCloudflareDNS(ctx, client, publicationID, createdRecordID); cleanupErr != nil {
						return cleanupErr
					}
				}
				return errStalePublicationReconcile
			}
			dnsRecordID = createdRecordID
		}
		if !(appKey == threeXUIAppKey && serviceName == "subscription") && accessApplicationID == "" {
			if err := s.ensureCloudflareServiceAccess(ctx, publicationID, revision, hostname); err != nil {
				return err
			}
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
			dnsRecordID, err = s.reusePublicationDNSRecord(ctx, publicationID, kind, gatewayID, hostname)
			if err != nil {
				return err
			}
		}
		if dnsRecordID == "" {
			createdRecordID, created, createErr := client.ensureDNSRecord(ctx, recordType, hostname, ip.String(), false)
			if createErr != nil {
				return createErr
			}
			updated, updateErr := s.db.ExecContext(ctx, `UPDATE publications SET dns_record_id = ?, updated_at = ? WHERE id = ? AND desired_revision = ? AND status <> 'stopped' AND dns_record_id = ''`, createdRecordID, s.now().UTC().Format(time.RFC3339Nano), publicationID, revision)
			if updateErr != nil {
				if created {
					return errors.Join(updateErr, s.compensateUntrackedCloudflareDNS(ctx, client, publicationID, createdRecordID))
				}
				return updateErr
			}
			if changed, _ := updated.RowsAffected(); changed != 1 {
				if created {
					if cleanupErr := s.compensateUntrackedCloudflareDNS(ctx, client, publicationID, createdRecordID); cleanupErr != nil {
						return cleanupErr
					}
				}
				return errStalePublicationReconcile
			}
		}
		return nil
	}
	return nil
}

func (s *Store) reusePublicationDNSRecord(ctx context.Context, publicationID, kind, gatewayID, hostname string) (string, error) {
	var recordID string
	err := s.db.QueryRowContext(ctx, `SELECT dns_record_id FROM publications
		WHERE id <> ? AND kind = ? AND COALESCE(gateway_node_id, '') = ? AND hostname = ?
		AND status <> 'stopped' AND dns_record_id <> '' ORDER BY created_at LIMIT 1`, publicationID, kind, gatewayID, hostname).Scan(&recordID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE publications SET dns_record_id = ?, updated_at = ? WHERE id = ? AND dns_record_id = ''`, recordID, s.now().UTC().Format(time.RFC3339Nano), publicationID); err != nil {
		return "", err
	}
	return recordID, nil
}

func (s *Store) ensureCloudflareServiceAccess(ctx context.Context, publicationID string, revision int64, hostname string) error {
	record, exists, err := s.centerRemoteAccessRecord(ctx)
	if err != nil {
		return err
	}
	if !exists || record.Status != "configured" || record.IdentityProviderID == "" {
		return errors.New("center: enable the Center Cloudflare Access entry before publishing protected Web services")
	}
	client, err := s.cloudflareWithScopes(ctx, cloudflareAccessScopes...)
	if err != nil {
		return err
	}
	applicationID, err := client.createAccessApplication(ctx, "Vastora "+hostname, hostname, record.AudienceKind, record.AudienceValue, record.IdentityProviderID)
	if err != nil {
		return err
	}
	updated, updateErr := s.db.ExecContext(ctx, `UPDATE publications SET access_application_id = ?, updated_at = ? WHERE id = ? AND desired_revision = ? AND status <> 'stopped' AND access_application_id = ''`, applicationID, s.now().UTC().Format(time.RFC3339Nano), publicationID, revision)
	if updateErr != nil {
		return errors.Join(updateErr, client.deleteAccessApplication(context.WithoutCancel(ctx), applicationID))
	}
	if changed, _ := updated.RowsAffected(); changed != 1 {
		return errors.Join(errStalePublicationReconcile, client.deleteAccessApplication(context.WithoutCancel(ctx), applicationID))
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
	var accessApplicationID string
	if err := s.db.QueryRowContext(ctx, `SELECT access_application_id FROM publications WHERE id = ?`, id).Scan(&accessApplicationID); err != nil {
		return err
	}
	client, err := s.cloudflare(ctx)
	cleanupErrors := []error{}
	if err != nil {
		cleanupErrors = append(cleanupErrors, err)
	} else if dnsRecordID != "" {
		var otherReferences int
		dnsRemoved := false
		if countErr := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE id <> ? AND status <> 'stopped' AND dns_record_id = ?`, id, dnsRecordID).Scan(&otherReferences); countErr != nil {
			cleanupErrors = append(cleanupErrors, countErr)
		} else if otherReferences == 0 {
			if deleteErr := client.deleteDNSRecord(ctx, dnsRecordID); deleteErr != nil {
				cleanupErrors = append(cleanupErrors, deleteErr)
			} else {
				dnsRemoved = true
			}
		} else {
			dnsRemoved = true
		}
		if dnsRemoved {
			_, err = s.db.ExecContext(ctx, `UPDATE publications SET dns_record_id = '', updated_at = ? WHERE id = ?`, s.now().UTC().Format(time.RFC3339Nano), id)
		}
		if err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if accessApplicationID != "" {
		var otherReferences int
		accessRemoved := false
		if countErr := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE id <> ? AND status <> 'stopped' AND access_application_id = ?`, id, accessApplicationID).Scan(&otherReferences); countErr != nil {
			cleanupErrors = append(cleanupErrors, countErr)
		} else if otherReferences == 0 {
			accessClient, accessErr := s.cloudflareWithScopes(ctx, cloudflareAccessScopes...)
			if accessErr != nil {
				cleanupErrors = append(cleanupErrors, accessErr)
			} else if deleteErr := accessClient.deleteAccessApplication(ctx, accessApplicationID); deleteErr != nil {
				cleanupErrors = append(cleanupErrors, deleteErr)
			} else {
				accessRemoved = true
			}
		} else {
			accessRemoved = true
		}
		if accessRemoved {
			if _, clearErr := s.db.ExecContext(ctx, `UPDATE publications SET access_application_id = '', updated_at = ? WHERE id = ?`, s.now().UTC().Format(time.RFC3339Nano), id); clearErr != nil {
				cleanupErrors = append(cleanupErrors, clearErr)
			}
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
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT p.hostname FROM publications p JOIN services s ON s.id = p.service_id WHERE p.gateway_node_id = ? AND p.kind = 'cloudflare_tunnel' AND p.status <> 'stopped' AND s.status <> 'stopped' ORDER BY p.hostname`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []TunnelTaskIngress{}
	for rows.Next() {
		var value TunnelTaskIngress
		if err := rows.Scan(&value.Hostname); err != nil {
			return nil, err
		}
		httpPort, _, _ := gatewayruntime.CaddyListenerPorts("system")
		value.Service = fmt.Sprintf("http://%s:%d", dockerruntime.CaddyAlias, httpPort)
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

type cloudflareTunnelRecord struct {
	ID         string `json:"id"`
	AccountTag string `json:"account_tag"`
	Name       string `json:"name"`
	ConfigSrc  string `json:"config_src"`
}

func (client cloudflareClient) createTunnel(ctx context.Context, name, tunnelSecret string) (cloudflareTunnelRecord, error) {
	body := map[string]any{"name": name, "config_src": "cloudflare"}
	if tunnelSecret != "" {
		body["tunnel_secret"] = tunnelSecret
	}
	var result cloudflareTunnelRecord
	err := client.do(ctx, http.MethodPost, "/accounts/"+url.PathEscape(client.accountID)+"/cfd_tunnel", body, &result)
	if err != nil {
		return cloudflareTunnelRecord{}, fmt.Errorf("center: create Cloudflare Tunnel: %w", err)
	}
	if result.ID == "" {
		return cloudflareTunnelRecord{}, errors.New("center: Cloudflare did not return a Tunnel ID")
	}
	return result, nil
}

func (client cloudflareClient) listTunnelsByName(ctx context.Context, name string) ([]cloudflareTunnelRecord, error) {
	query := url.Values{}
	query.Set("is_deleted", "false")
	query.Set("name", name)
	query.Set("per_page", "100")
	var result []cloudflareTunnelRecord
	if err := client.do(ctx, http.MethodGet, "/accounts/"+url.PathEscape(client.accountID)+"/cfd_tunnel?"+query.Encode(), nil, &result); err != nil {
		return nil, err
	}
	filtered := make([]cloudflareTunnelRecord, 0, len(result))
	for _, tunnel := range result {
		if tunnel.Name == name {
			filtered = append(filtered, tunnel)
		}
	}
	return filtered, nil
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

func (client cloudflareClient) deleteTunnel(ctx context.Context, tunnelID string) error {
	var ignored json.RawMessage
	return client.do(ctx, http.MethodDelete, "/accounts/"+url.PathEscape(client.accountID)+"/cfd_tunnel/"+url.PathEscape(tunnelID), nil, &ignored)
}

type cloudflareAccessIdentityProvider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func (client cloudflareClient) ensureAccessOrganization(ctx context.Context) error {
	path := "/accounts/" + url.PathEscape(client.accountID) + "/access/organizations"
	var organization struct {
		AuthDomain string `json:"auth_domain"`
	}
	err := client.do(ctx, http.MethodGet, path, nil, &organization)
	if err == nil && strings.TrimSpace(organization.AuthDomain) != "" {
		return nil
	}
	var apiFailure *cloudflareAPIError
	if err == nil || !errors.As(err, &apiFailure) || apiFailure.StatusCode != http.StatusNotFound {
		if err == nil {
			return errors.New("center: Cloudflare Access organization has no authentication domain")
		}
		return fmt.Errorf("center: inspect Cloudflare Access organization: %w", err)
	}
	authLabel := "vastora-" + sanitizeCloudflareName(client.accountID)
	if len(authLabel) > 32 {
		authLabel = authLabel[:32]
	}
	body := map[string]any{
		"auth_domain":               authLabel + ".cloudflareaccess.com",
		"name":                      "Vastora",
		"session_duration":          "24h",
		"auto_redirect_to_identity": true,
	}
	if err := client.do(ctx, http.MethodPost, path, body, &organization); err != nil {
		return fmt.Errorf("center: create Cloudflare Access organization: %w", err)
	}
	if strings.TrimSpace(organization.AuthDomain) == "" {
		return errors.New("center: Cloudflare did not return an Access authentication domain")
	}
	return nil
}

func (client cloudflareClient) ensureOneTimePINIdentityProvider(ctx context.Context) (string, error) {
	var providers []cloudflareAccessIdentityProvider
	path := "/accounts/" + url.PathEscape(client.accountID) + "/access/identity_providers"
	if err := client.do(ctx, http.MethodGet, path, nil, &providers); err != nil {
		return "", fmt.Errorf("center: list Cloudflare Access identity providers: %w", err)
	}
	for _, provider := range providers {
		if provider.Type == "onetimepin" && provider.ID != "" {
			return provider.ID, nil
		}
	}
	var created cloudflareAccessIdentityProvider
	if err := client.do(ctx, http.MethodPost, path, map[string]any{"name": "Vastora one-time PIN", "type": "onetimepin", "config": map[string]any{}}, &created); err != nil {
		return "", fmt.Errorf("center: create Cloudflare Access one-time PIN provider: %w", err)
	}
	if created.ID == "" {
		return "", errors.New("center: Cloudflare did not return an Access identity provider ID")
	}
	return created.ID, nil
}

func (client cloudflareClient) createAccessApplication(ctx context.Context, name, domain, audienceKind, audienceValue, identityProviderID string) (string, error) {
	selector := map[string]any{}
	switch audienceKind {
	case "email":
		selector["email"] = map[string]string{"email": audienceValue}
	case "email_domain":
		selector["email_domain"] = map[string]string{"domain": audienceValue}
	default:
		return "", errors.New("center: unsupported Cloudflare Access audience")
	}
	body := map[string]any{
		"name":                      name,
		"domain":                    domain,
		"type":                      "self_hosted",
		"session_duration":          "24h",
		"auto_redirect_to_identity": true,
		"allowed_idps":              []string{identityProviderID},
		"policies": []map[string]any{{
			"name":       name + " users",
			"decision":   "allow",
			"precedence": 1,
			"include":    []map[string]any{selector},
			"require": []map[string]any{{
				"login_method": map[string]string{"id": identityProviderID},
			}},
		}},
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := client.do(ctx, http.MethodPost, "/accounts/"+url.PathEscape(client.accountID)+"/access/apps", body, &result); err != nil {
		return "", fmt.Errorf("center: create Cloudflare Access application: %w", err)
	}
	if result.ID == "" {
		return "", errors.New("center: Cloudflare did not return an Access application ID")
	}
	return result.ID, nil
}

func (client cloudflareClient) deleteAccessApplication(ctx context.Context, applicationID string) error {
	var ignored json.RawMessage
	return client.do(ctx, http.MethodDelete, "/accounts/"+url.PathEscape(client.accountID)+"/access/apps/"+url.PathEscape(applicationID), nil, &ignored)
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

func (client cloudflareClient) ensureDNSRecord(ctx context.Context, recordType, name, content string, proxied bool) (string, bool, error) {
	existing, err := client.listDNSRecords(ctx, name)
	if err != nil {
		return "", false, err
	}
	if recordID, found, conflict := matchingCloudflareDNSRecord(existing, recordType, name, content, proxied); found || conflict {
		if conflict {
			return "", false, fmt.Errorf("center: DNS record %s already exists with a different value", name)
		}
		return recordID, false, nil
	}
	recordID, err := client.createDNSRecord(ctx, recordType, name, content, proxied)
	if err == nil {
		return recordID, true, nil
	}
	// A create response can be lost after Cloudflare commits the record, and a
	// concurrent reconciliation can win the same race. Re-read authoritative
	// state before reporting failure or attempting compensation.
	existing, listErr := client.listDNSRecords(ctx, name)
	if listErr == nil {
		if observedID, found, conflict := matchingCloudflareDNSRecord(existing, recordType, name, content, proxied); found {
			return observedID, false, nil
		} else if conflict {
			return "", false, fmt.Errorf("center: DNS record %s already exists with a different value", name)
		}
	}
	return "", false, err
}

func matchingCloudflareDNSRecord(records []cloudflareDNSRecord, recordType, name, content string, proxied bool) (string, bool, bool) {
	if len(records) == 0 {
		return "", false, false
	}
	if len(records) == 1 && records[0].Type == recordType && records[0].Name == name && records[0].Content == content && records[0].Proxied == proxied && records[0].ID != "" {
		return records[0].ID, true, false
	}
	return "", false, true
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
		code := 0
		if len(envelope.Errors) != 0 {
			message = envelope.Errors[0].Message
			code = envelope.Errors[0].Code
		}
		return &cloudflareAPIError{StatusCode: response.StatusCode, Code: code, Message: message}
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
