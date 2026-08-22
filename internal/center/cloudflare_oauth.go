package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

const (
	cloudflareOAuthClientID    = "565bf36df0a8deb0fde1bd27367a44bd"
	cloudflareOAuthRedirectURI = "https://vastora.petauron.com/oauth/cloudflare/callback"
	cloudflareOAuthLifetime    = 10 * time.Minute
)

type cloudflareOAuthConfig struct {
	ClientID         string
	AuthorizationURL string
	TokenURL         string
	RelayURL         string
	APIURL           string
	HTTPClient       *http.Client
}

type cloudflareOAuthSession struct {
	State        string
	PollSecret   string
	PKCEVerifier string
	Token        cloudflareOAuthToken
	Zones        []CloudflareZone
	ExpiresAt    time.Time
}

type cloudflareOAuthToken struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	TokenType    string    `json:"tokenType"`
	Scope        string    `json:"scope"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type cloudflareTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

type CloudflareOAuthStart struct {
	SessionID        string    `json:"sessionId"`
	AuthorizationURL string    `json:"authorizationUrl"`
	ExpiresAt        time.Time `json:"expiresAt"`
}

type CloudflareZone struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AccountID   string `json:"accountId"`
	AccountName string `json:"accountName"`
}

type CloudflareOAuthPoll struct {
	Status string           `json:"status"`
	Zones  []CloudflareZone `json:"zones,omitempty"`
}

type SetupDNSInput struct {
	CenterURL     string `json:"centerUrl"`
	HeadscaleURL  string `json:"headscaleUrl,omitempty"`
	PublicAddress string `json:"publicAddress"`
}

type SetupDNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

func defaultCloudflareOAuthConfig() cloudflareOAuthConfig {
	return cloudflareOAuthConfig{
		ClientID:         cloudflareOAuthClientID,
		AuthorizationURL: "https://dash.cloudflare.com/oauth2/auth",
		TokenURL:         "https://dash.cloudflare.com/oauth2/token",
		RelayURL:         "https://vastora.petauron.com/oauth/cloudflare",
		APIURL:           cloudflareAPIURL,
		HTTPClient:       &http.Client{Timeout: 20 * time.Second},
	}
}

func (s *Store) CloudflareOAuthAvailable() bool {
	return strings.TrimSpace(s.cloudflareOAuth.ClientID) != ""
}

func (s *Store) StartCloudflareOAuth() (CloudflareOAuthStart, error) {
	config := s.cloudflareOAuth
	if strings.TrimSpace(config.ClientID) == "" {
		return CloudflareOAuthStart{}, errors.New("center: Cloudflare OAuth is unavailable in this build")
	}
	sessionID, err := randomToken(24)
	if err != nil {
		return CloudflareOAuthStart{}, err
	}
	pollSecret, err := randomToken(32)
	if err != nil {
		return CloudflareOAuthStart{}, err
	}
	verifier, err := randomToken(48)
	if err != nil {
		return CloudflareOAuthStart{}, err
	}
	commitment := oauthSHA256(pollSecret)
	state := "v1." + sessionID + "." + commitment
	expiresAt := s.now().UTC().Add(cloudflareOAuthLifetime)
	s.cloudflareOAuthMu.Lock()
	s.cleanupCloudflareOAuthSessionsLocked(s.now().UTC())
	if len(s.cloudflareOAuthSessions) >= 20 {
		s.cloudflareOAuthMu.Unlock()
		return CloudflareOAuthStart{}, errors.New("center: too many Cloudflare authorization attempts are active")
	}
	s.cloudflareOAuthSessions[sessionID] = &cloudflareOAuthSession{State: state, PollSecret: pollSecret, PKCEVerifier: verifier, ExpiresAt: expiresAt}
	s.cloudflareOAuthMu.Unlock()

	values := url.Values{
		"client_id":             {config.ClientID},
		"redirect_uri":          {cloudflareOAuthRedirectURI},
		"response_type":         {"code"},
		"scope":                 {"zone.read dns.write argotunnel.write offline_access"},
		"state":                 {state},
		"code_challenge":        {oauthSHA256(verifier)},
		"code_challenge_method": {"S256"},
	}
	return CloudflareOAuthStart{SessionID: sessionID, AuthorizationURL: config.AuthorizationURL + "?" + values.Encode(), ExpiresAt: expiresAt}, nil
}

func (s *Store) PollCloudflareOAuth(ctx context.Context, sessionID string) (CloudflareOAuthPoll, error) {
	sessionID = strings.TrimSpace(sessionID)
	s.cloudflareOAuthMu.Lock()
	s.cleanupCloudflareOAuthSessionsLocked(s.now().UTC())
	session := s.cloudflareOAuthSessions[sessionID]
	if session == nil {
		s.cloudflareOAuthMu.Unlock()
		return CloudflareOAuthPoll{}, errors.New("center: Cloudflare authorization expired; connect again")
	}
	if session.Token.AccessToken != "" {
		zones := slices.Clone(session.Zones)
		s.cloudflareOAuthMu.Unlock()
		return CloudflareOAuthPoll{Status: "authorized", Zones: zones}, nil
	}
	state, pollSecret, verifier := session.State, session.PollSecret, session.PKCEVerifier
	s.cloudflareOAuthMu.Unlock()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.cloudflareOAuth.RelayURL, "/")+"/sessions/"+url.PathEscape(state), nil)
	if err != nil {
		return CloudflareOAuthPoll{}, err
	}
	request.Header.Set("Authorization", "Bearer "+pollSecret)
	response, err := s.cloudflareOAuth.HTTPClient.Do(request)
	if err != nil {
		return CloudflareOAuthPoll{}, fmt.Errorf("center: poll Cloudflare authorization: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return CloudflareOAuthPoll{}, err
	}
	if response.StatusCode == http.StatusAccepted {
		return CloudflareOAuthPoll{Status: "pending"}, nil
	}
	var relay struct {
		Status           string `json:"status"`
		Code             string `json:"code"`
		Error            string `json:"error"`
		ErrorDescription string `json:"errorDescription"`
	}
	if err := json.Unmarshal(raw, &relay); err != nil {
		return CloudflareOAuthPoll{}, fmt.Errorf("center: OAuth relay returned HTTP %d", response.StatusCode)
	}
	if response.StatusCode != http.StatusOK || relay.Status != "authorized" || relay.Code == "" {
		message := strings.TrimSpace(relay.ErrorDescription)
		if message == "" {
			message = strings.TrimSpace(relay.Error)
		}
		if message == "" {
			message = fmt.Sprintf("OAuth relay returned HTTP %d", response.StatusCode)
		}
		return CloudflareOAuthPoll{}, errors.New("center: Cloudflare authorization failed: " + message)
	}
	token, err := s.exchangeCloudflareCode(ctx, relay.Code, verifier)
	if err != nil {
		return CloudflareOAuthPoll{}, err
	}
	zones, err := s.listCloudflareZones(ctx, token.AccessToken)
	if err != nil {
		return CloudflareOAuthPoll{}, err
	}
	if len(zones) == 0 {
		return CloudflareOAuthPoll{}, errors.New("center: Cloudflare authorization has no accessible zones")
	}
	s.cloudflareOAuthMu.Lock()
	if current := s.cloudflareOAuthSessions[sessionID]; current != nil && current.State == state {
		current.Token = token
		current.Zones = slices.Clone(zones)
	}
	s.cloudflareOAuthMu.Unlock()
	return CloudflareOAuthPoll{Status: "authorized", Zones: zones}, nil
}

func (s *Store) CompleteCloudflareOAuth(ctx context.Context, sessionID, zoneID string) (IntegrationView, error) {
	sessionID = strings.TrimSpace(sessionID)
	zoneID = strings.TrimSpace(zoneID)
	s.cloudflareOAuthMu.Lock()
	s.cleanupCloudflareOAuthSessionsLocked(s.now().UTC())
	session := s.cloudflareOAuthSessions[sessionID]
	if session == nil || session.Token.AccessToken == "" {
		s.cloudflareOAuthMu.Unlock()
		return IntegrationView{}, errors.New("center: Cloudflare authorization is not complete")
	}
	var selected CloudflareZone
	for _, zone := range session.Zones {
		if zone.ID == zoneID {
			selected = zone
			break
		}
	}
	token := session.Token
	s.cloudflareOAuthMu.Unlock()
	if selected.ID == "" {
		return IntegrationView{}, errors.New("center: selected Cloudflare zone was not authorized")
	}
	client := cloudflareClient{accountID: selected.AccountID, zoneID: selected.ID, token: token.AccessToken, baseURL: s.cloudflareOAuth.APIURL, http: s.cloudflareOAuth.HTTPClient}
	zoneName, err := client.verify(ctx)
	if err != nil {
		return IntegrationView{}, err
	}
	encoded, err := json.Marshal(token)
	if err != nil {
		return IntegrationView{}, err
	}
	existingSecretID, _, err := s.integrationSecret(ctx, "cloudflare")
	if err != nil {
		return IntegrationView{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IntegrationView{}, err
	}
	defer tx.Rollback()
	secretID, err := s.putSecret(ctx, tx, encoded, "integration:cloudflare")
	if err != nil {
		return IntegrationView{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO network_integrations(kind, mode, endpoint, account_id, zone_id, secret_id, status, created_at, updated_at)
		VALUES('cloudflare', 'oauth', ?, ?, ?, ?, 'configured', ?, ?)
		ON CONFLICT(kind) DO UPDATE SET mode = 'oauth', endpoint = excluded.endpoint, account_id = excluded.account_id, zone_id = excluded.zone_id, secret_id = excluded.secret_id, status = 'configured', last_error = '', updated_at = excluded.updated_at`, zoneName, selected.AccountID, selected.ID, secretID, now, now); err != nil {
		return IntegrationView{}, fmt.Errorf("center: save Cloudflare OAuth integration: %w", err)
	}
	if existingSecretID != "" && existingSecretID != secretID {
		if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, existingSecretID); err != nil {
			return IntegrationView{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return IntegrationView{}, err
	}
	s.cloudflareOAuthMu.Lock()
	delete(s.cloudflareOAuthSessions, sessionID)
	s.cloudflareOAuthMu.Unlock()
	return s.Integration(ctx, "cloudflare")
}

func (s *Store) ConfigureSetupDNS(ctx context.Context, input SetupDNSInput, candidates []networking.Candidate) ([]SetupDNSRecord, error) {
	publicIP := net.ParseIP(strings.TrimSpace(input.PublicAddress))
	if publicIP == nil {
		return nil, errors.New("center: select a discovered public address")
	}
	validPublic := false
	for _, candidate := range candidates {
		if candidate.Kind == networking.KindPublic && candidate.Address == publicIP.String() {
			validPublic = true
			break
		}
	}
	if !validPublic {
		return nil, errors.New("center: public address is not assigned to this Center server")
	}
	hostnames := make([]string, 0, 2)
	headscaleHostname := ""
	for _, rawURL := range []string{input.CenterURL, input.HeadscaleURL} {
		if strings.TrimSpace(rawURL) == "" {
			continue
		}
		parsed, err := url.Parse(strings.TrimSpace(rawURL))
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || net.ParseIP(parsed.Hostname()) != nil {
			return nil, errors.New("center: automatic DNS requires HTTPS hostnames")
		}
		hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
		if !domainSuffixPattern.MatchString(hostname) {
			return nil, errors.New("center: automatic DNS hostname is invalid")
		}
		if !slices.Contains(hostnames, hostname) {
			hostnames = append(hostnames, hostname)
		}
		if strings.TrimSpace(rawURL) == strings.TrimSpace(input.HeadscaleURL) && strings.TrimSpace(input.HeadscaleURL) != "" {
			headscaleHostname = hostname
		}
	}
	if len(hostnames) == 0 {
		return nil, errors.New("center: automatic DNS requires at least one hostname")
	}
	client, err := s.cloudflare(ctx)
	if err != nil {
		return nil, err
	}
	var zoneName string
	if err := s.db.QueryRowContext(ctx, `SELECT endpoint FROM network_integrations WHERE kind = 'cloudflare' AND status = 'configured'`).Scan(&zoneName); err != nil {
		return nil, err
	}
	recordType := "AAAA"
	if publicIP.To4() != nil {
		recordType = "A"
	}
	publicHostnames := hostnames
	if headscaleHostname != "" {
		publicHostnames = []string{headscaleHostname}
	}
	createdIDs := []string{}
	rollback := func() {
		for _, id := range createdIDs {
			_ = client.deleteDNSRecord(context.Background(), id)
		}
	}
	for _, hostname := range hostnames {
		if hostname != zoneName && !strings.HasSuffix(hostname, "."+zoneName) {
			rollback()
			return nil, fmt.Errorf("center: %s is outside the selected Cloudflare zone %s", hostname, zoneName)
		}
	}
	result := make([]SetupDNSRecord, 0, len(publicHostnames))
	for _, hostname := range publicHostnames {
		existing, err := client.listDNSRecords(ctx, hostname)
		if err != nil {
			rollback()
			return nil, err
		}
		if len(existing) != 0 {
			if len(existing) == 1 && existing[0].Type == recordType && existing[0].Content == publicIP.String() && !existing[0].Proxied {
				result = append(result, SetupDNSRecord{ID: existing[0].ID, Type: recordType, Name: hostname, Content: publicIP.String()})
				continue
			}
			rollback()
			return nil, fmt.Errorf("center: DNS record %s already exists with a different value; Vastora did not overwrite it", hostname)
		}
		id, err := client.createDNSRecord(ctx, recordType, hostname, publicIP.String(), false)
		if err != nil {
			rollback()
			return nil, err
		}
		createdIDs = append(createdIDs, id)
		result = append(result, SetupDNSRecord{ID: id, Type: recordType, Name: hostname, Content: publicIP.String()})
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		rollback()
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES('cloudflare_setup_dns_records', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(encoded)); err != nil {
		rollback()
		return nil, err
	}
	return result, nil
}

func (s *Store) removePublicCenterSetupDNS(ctx context.Context, centerURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(centerURL))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return errors.New("center: private Center DNS hostname is invalid")
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	var encoded string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'cloudflare_setup_dns_records'`).Scan(&encoded); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	var records []SetupDNSRecord
	if json.Unmarshal([]byte(encoded), &records) != nil {
		return errors.New("center: stored setup DNS records are invalid")
	}
	remaining := make([]SetupDNSRecord, 0, len(records))
	client, err := s.cloudflare(ctx)
	if err != nil {
		return err
	}
	for _, tracked := range records {
		if tracked.Name != hostname {
			remaining = append(remaining, tracked)
			continue
		}
		current, err := client.listDNSRecords(ctx, hostname)
		if err != nil {
			return err
		}
		if len(current) == 0 {
			continue
		}
		if len(current) != 1 || current[0].ID != tracked.ID || current[0].Type != tracked.Type || current[0].Content != tracked.Content || current[0].Proxied {
			return fmt.Errorf("center: public Center DNS record %s changed outside Vastora; remove it manually before completing private access", hostname)
		}
		for _, candidate := range current {
			if err := client.deleteDNSRecord(ctx, candidate.ID); err != nil {
				return err
			}
		}
	}
	updated, err := json.Marshal(remaining)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE settings SET value = ? WHERE key = 'cloudflare_setup_dns_records'`, string(updated)); err != nil {
		return err
	}
	return nil
}

func (s *Store) exchangeCloudflareCode(ctx context.Context, code, verifier string) (cloudflareOAuthToken, error) {
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {s.cloudflareOAuth.ClientID},
		"redirect_uri":  {cloudflareOAuthRedirectURI},
		"code_verifier": {verifier},
	}
	response, err := s.postCloudflareToken(ctx, values)
	if err != nil {
		return cloudflareOAuthToken{}, err
	}
	return s.normalizeCloudflareToken(response, "")
}

func (s *Store) refreshCloudflareToken(ctx context.Context, refreshToken string) (cloudflareOAuthToken, error) {
	values := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}, "client_id": {s.cloudflareOAuth.ClientID}}
	response, err := s.postCloudflareToken(ctx, values)
	if err != nil {
		return cloudflareOAuthToken{}, err
	}
	return s.normalizeCloudflareToken(response, refreshToken)
}

func (s *Store) postCloudflareToken(ctx context.Context, values url.Values) (cloudflareTokenResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cloudflareOAuth.TokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return cloudflareTokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := s.cloudflareOAuth.HTTPClient.Do(request)
	if err != nil {
		return cloudflareTokenResponse{}, fmt.Errorf("center: exchange Cloudflare OAuth token: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 256<<10))
	if err != nil {
		return cloudflareTokenResponse{}, err
	}
	var token cloudflareTokenResponse
	if err := json.Unmarshal(raw, &token); err != nil {
		return cloudflareTokenResponse{}, fmt.Errorf("center: Cloudflare token endpoint returned HTTP %d", response.StatusCode)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || token.Error != "" {
		message := strings.TrimSpace(token.Description)
		if message == "" {
			message = strings.TrimSpace(token.Error)
		}
		if message == "" {
			message = response.Status
		}
		return cloudflareTokenResponse{}, errors.New("center: Cloudflare token exchange failed: " + message)
	}
	return token, nil
}

func (s *Store) normalizeCloudflareToken(response cloudflareTokenResponse, previousRefresh string) (cloudflareOAuthToken, error) {
	if strings.TrimSpace(response.AccessToken) == "" || response.ExpiresIn <= 0 {
		return cloudflareOAuthToken{}, errors.New("center: Cloudflare returned an incomplete OAuth token")
	}
	refresh := strings.TrimSpace(response.RefreshToken)
	if refresh == "" {
		refresh = previousRefresh
	}
	if refresh == "" {
		return cloudflareOAuthToken{}, errors.New("center: Cloudflare did not return a refresh token")
	}
	return cloudflareOAuthToken{AccessToken: response.AccessToken, RefreshToken: refresh, TokenType: response.TokenType, Scope: response.Scope, ExpiresAt: s.now().UTC().Add(time.Duration(response.ExpiresIn) * time.Second)}, nil
}

func (s *Store) saveCloudflareOAuthToken(ctx context.Context, oldSecretID string, token cloudflareOAuthToken) error {
	encoded, err := json.Marshal(token)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	secretID, err := s.putSecret(ctx, tx, encoded, "integration:cloudflare")
	if err != nil {
		return err
	}
	if result, err := tx.ExecContext(ctx, `UPDATE network_integrations SET secret_id = ?, updated_at = ? WHERE kind = 'cloudflare' AND secret_id = ?`, secretID, s.now().UTC().Format(time.RFC3339Nano), oldSecretID); err != nil {
		return err
	} else if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("center: Cloudflare integration changed while refreshing OAuth")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, oldSecretID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) listCloudflareZones(ctx context.Context, token string) ([]CloudflareZone, error) {
	result := []CloudflareZone{}
	for page := 1; ; page++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.cloudflareOAuth.APIURL, "/")+"/zones?per_page=50&page="+fmt.Sprint(page), nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := s.cloudflareOAuth.HTTPClient.Do(request)
		if err != nil {
			return nil, fmt.Errorf("center: list Cloudflare zones: %w", err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		var envelope struct {
			Success bool              `json:"success"`
			Errors  []cloudflareError `json:"errors"`
			Result  []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Account struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				} `json:"account"`
			} `json:"result"`
			ResultInfo struct {
				TotalPages int `json:"total_pages"`
			} `json:"result_info"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil || response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.Success {
			message := response.Status
			if len(envelope.Errors) != 0 {
				message = envelope.Errors[0].Message
			}
			return nil, errors.New("center: list Cloudflare zones: " + message)
		}
		for _, zone := range envelope.Result {
			name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zone.Name), "."))
			if zone.ID != "" && zone.Account.ID != "" && domainSuffixPattern.MatchString(name) {
				result = append(result, CloudflareZone{ID: zone.ID, Name: name, AccountID: zone.Account.ID, AccountName: zone.Account.Name})
			}
		}
		if page >= envelope.ResultInfo.TotalPages || envelope.ResultInfo.TotalPages == 0 {
			break
		}
	}
	slices.SortFunc(result, func(left, right CloudflareZone) int { return strings.Compare(left.Name, right.Name) })
	return result, nil
}

func (s *Store) cleanupCloudflareOAuthSessionsLocked(now time.Time) {
	for id, session := range s.cloudflareOAuthSessions {
		if !now.Before(session.ExpiresAt) {
			delete(s.cloudflareOAuthSessions, id)
		}
	}
}

func oauthSHA256(value string) string {
	digest := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

type cloudflareDNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
}

func (client cloudflareClient) listDNSRecords(ctx context.Context, hostname string) ([]cloudflareDNSRecord, error) {
	var result []cloudflareDNSRecord
	path := "/zones/" + url.PathEscape(client.zoneID) + "/dns_records?name=" + url.QueryEscape(hostname) + "&per_page=100"
	if err := client.do(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, fmt.Errorf("center: list Cloudflare DNS records: %w", err)
	}
	return result, nil
}
