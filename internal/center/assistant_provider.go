package center

import (
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

const assistantProviderSecretData = "assistant-provider:default"

type AssistantProviderInput struct {
	APIURL       string `json:"apiUrl"`
	APIKey       string `json:"apiKey"`
	Model        string `json:"model"`
	AllowPrivate bool   `json:"allowPrivate"`
}

type AssistantProviderView struct {
	APIURL       string    `json:"apiUrl"`
	Model        string    `json:"model"`
	APIKeySet    bool      `json:"apiKeySet"`
	AllowPrivate bool      `json:"allowPrivate"`
	Status       string    `json:"status"`
	LastError    string    `json:"lastError,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type assistantProvider struct {
	AssistantProviderView
	APIKey string
}

func (s *Store) AssistantProvider(ctx context.Context) (AssistantProviderView, error) {
	var view AssistantProviderView
	var secretID, updatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT api_url, model, api_key_secret_id, allow_private, status, last_error, updated_at FROM assistant_model_providers WHERE id = 1`).Scan(&view.APIURL, &view.Model, &secretID, &view.AllowPrivate, &view.Status, &view.LastError, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AssistantProviderView{Status: "disabled"}, nil
	}
	if err != nil {
		return AssistantProviderView{}, fmt.Errorf("center: read assistant provider: %w", err)
	}
	view.APIKeySet = secretID != ""
	view.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return view, nil
}

func (s *Store) SaveAssistantProvider(ctx context.Context, input AssistantProviderInput) (AssistantProviderView, error) {
	apiURL, err := normalizeAssistantAPIURL(ctx, input.APIURL, input.AllowPrivate, s.assistantResolve)
	if err != nil {
		return AssistantProviderView{}, err
	}
	input.Model = strings.TrimSpace(input.Model)
	if input.Model == "" || len(input.Model) > 200 || strings.ContainsAny(input.Model, "\r\n\t") {
		return AssistantProviderView{}, errors.New("center: assistant model identifier is invalid")
	}
	input.APIKey = strings.TrimSpace(input.APIKey)
	if len(input.APIKey) > 8192 {
		return AssistantProviderView{}, errors.New("center: assistant API key is too large")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssistantProviderView{}, fmt.Errorf("center: begin assistant provider update: %w", err)
	}
	defer tx.Rollback()
	var previousSecretID string
	err = tx.QueryRowContext(ctx, `SELECT api_key_secret_id FROM assistant_model_providers WHERE id = 1`).Scan(&previousSecretID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AssistantProviderView{}, fmt.Errorf("center: inspect assistant provider: %w", err)
	}
	secretID := previousSecretID
	if input.APIKey != "" {
		secretID, err = s.putSecret(ctx, tx, []byte(input.APIKey), assistantProviderSecretData)
		if err != nil {
			return AssistantProviderView{}, err
		}
	}
	if secretID == "" {
		return AssistantProviderView{}, errors.New("center: assistant API key is required")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO assistant_model_providers(id, api_url, model, api_key_secret_id, allow_private, status, last_error, created_at, updated_at)
		VALUES(1, ?, ?, ?, ?, 'configured', '', ?, ?)
		ON CONFLICT(id) DO UPDATE SET api_url = excluded.api_url, model = excluded.model, api_key_secret_id = excluded.api_key_secret_id,
		allow_private = excluded.allow_private, status = 'configured', last_error = '', updated_at = excluded.updated_at`, apiURL, input.Model, secretID, input.AllowPrivate, now, now); err != nil {
		return AssistantProviderView{}, fmt.Errorf("center: save assistant provider: %w", err)
	}
	if previousSecretID != "" && previousSecretID != secretID {
		if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, previousSecretID); err != nil {
			return AssistantProviderView{}, fmt.Errorf("center: replace assistant provider secret: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return AssistantProviderView{}, fmt.Errorf("center: commit assistant provider: %w", err)
	}
	return s.AssistantProvider(ctx)
}

func (s *Store) assistantProviderCredentials(ctx context.Context) (assistantProvider, error) {
	var provider assistantProvider
	var secretID, updatedAt string
	if err := s.db.QueryRowContext(ctx, `SELECT api_url, model, api_key_secret_id, allow_private, status, last_error, updated_at FROM assistant_model_providers WHERE id = 1`).Scan(&provider.APIURL, &provider.Model, &secretID, &provider.AllowPrivate, &provider.Status, &provider.LastError, &updatedAt); errors.Is(err, sql.ErrNoRows) {
		return assistantProvider{}, errors.New("center: assistant model provider is not configured")
	} else if err != nil {
		return assistantProvider{}, fmt.Errorf("center: read assistant provider: %w", err)
	}
	key, err := s.getSecret(ctx, secretID, assistantProviderSecretData)
	if err != nil {
		return assistantProvider{}, err
	}
	provider.APIKey = string(key)
	provider.APIKeySet = true
	provider.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return provider, nil
}

func (s *Store) ValidateAssistantProvider(ctx context.Context) (AssistantProviderView, error) {
	provider, err := s.assistantProviderCredentials(ctx)
	if err != nil {
		return AssistantProviderView{}, err
	}
	endpoint, _ := url.JoinPath(provider.APIURL, "models")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return AssistantProviderView{}, err
	}
	request.Header.Set("Authorization", "Bearer "+provider.APIKey)
	response, err := s.assistantHTTPClient(provider).Do(request)
	validationErr := err
	if err == nil {
		defer response.Body.Close()
		_, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if readErr != nil {
			validationErr = readErr
		} else if response.StatusCode < 200 || response.StatusCode >= 300 {
			validationErr = fmt.Errorf("provider returned HTTP %d", response.StatusCode)
		}
	}
	status, message := "verified", ""
	if validationErr != nil {
		status, message = "failed", redactedAssistantError(validationErr)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE assistant_model_providers SET status = ?, last_error = ?, updated_at = ? WHERE id = 1`, status, message, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return AssistantProviderView{}, fmt.Errorf("center: save assistant provider validation: %w", err)
	}
	view, readErr := s.AssistantProvider(ctx)
	if validationErr != nil {
		return view, fmt.Errorf("center: validate assistant provider: %s", message)
	}
	return view, readErr
}

func normalizeAssistantAPIURL(ctx context.Context, raw string, allowPrivate bool, resolve func(context.Context, string) ([]net.IPAddr, error)) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", errors.New("center: assistant API URL must be an exact credential-free HTTP(S) URL")
	}
	if parsed.Scheme == "http" && !allowPrivate {
		return "", errors.New("center: assistant API URL requires HTTPS")
	}
	addresses, err := resolve(ctx, parsed.Hostname())
	if err != nil || len(addresses) == 0 {
		return "", errors.New("center: assistant API host could not be resolved")
	}
	for _, address := range addresses {
		if err := validateAssistantProviderIP(address.IP, allowPrivate, parsed.Scheme); err != nil {
			return "", err
		}
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func validateAssistantProviderIP(ip net.IP, allowPrivate bool, scheme string) error {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || isAssistantMetadataIP(ip) {
		return errors.New("center: assistant API URL resolves to a disallowed address")
	}
	private := ip.IsPrivate() || ip.IsLoopback()
	if private && !allowPrivate {
		return errors.New("center: assistant API URL resolves to a private address; explicitly trust a private provider to continue")
	}
	if scheme == "http" && !private {
		return errors.New("center: plaintext assistant API is allowed only for an explicitly trusted private provider")
	}
	return nil
}

func isAssistantMetadataIP(ip net.IP) bool {
	for _, raw := range []string{"169.254.169.254", "100.100.100.200", "fd00:ec2::254"} {
		if ip.Equal(net.ParseIP(raw)) {
			return true
		}
	}
	return false
}

func (s *Store) assistantHTTPClient(provider assistantProvider) *http.Client {
	parsed, _ := url.Parse(provider.APIURL)
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	transport := &http.Transport{ForceAttemptHTTP2: true, TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 20 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || !strings.EqualFold(strings.TrimSuffix(host, "."), strings.TrimSuffix(parsed.Hostname(), ".")) {
			return nil, errors.New("center: assistant provider attempted an unexpected connection")
		}
		addresses, err := s.assistantResolve(ctx, host)
		if err != nil || len(addresses) == 0 {
			return nil, errors.New("center: assistant provider host could not be resolved")
		}
		for _, candidate := range addresses {
			if err := validateAssistantProviderIP(candidate.IP, provider.AllowPrivate, parsed.Scheme); err != nil {
				return nil, err
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
	}
	return &http.Client{Timeout: 45 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func redactedAssistantError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(redactAssistantText(err.Error()))
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}

func assistantJSON(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}
