package center

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const headscaleDNSFile = "headscale-extra-records.json"

type HeadscaleInput struct {
	Mode   string `json:"mode"`
	URL    string `json:"url"`
	APIKey string `json:"apiKey"`
}

type HeadscaleJoin struct {
	AgentID   string    `json:"agentId,omitempty"`
	Command   string    `json:"command"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type headscaleClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func (s *Store) ConfigureHeadscale(ctx context.Context, input HeadscaleInput) (IntegrationView, error) {
	input.Mode = strings.TrimSpace(input.Mode)
	input.APIKey = strings.TrimSpace(input.APIKey)
	if input.Mode != "builtin" && input.Mode != "external" {
		return IntegrationView{}, errors.New("center: Headscale mode must be builtin or external")
	}
	endpoint, err := s.authorizedHeadscaleEndpoint(input.URL)
	if err != nil {
		return IntegrationView{}, err
	}
	existingSecretID, existingAPIKey, err := s.integrationSecret(ctx, "headscale")
	if err != nil {
		return IntegrationView{}, err
	}
	replacingSecret := input.APIKey != ""
	if !replacingSecret {
		input.APIKey = existingAPIKey
	}
	if len(input.APIKey) < 20 {
		return IntegrationView{}, errors.New("center: Headscale API key is required")
	}
	client := headscaleClient{baseURL: endpoint, apiKey: input.APIKey, http: s.headscaleHTTPClient}
	if err := client.verify(ctx); err != nil {
		return IntegrationView{}, fmt.Errorf("center: verify Headscale: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IntegrationView{}, err
	}
	defer tx.Rollback()
	secretID := existingSecretID
	if replacingSecret {
		secretID, err = s.putSecret(ctx, tx, []byte(input.APIKey), "integration:headscale")
		if err != nil {
			return IntegrationView{}, err
		}
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO network_integrations(kind, mode, endpoint, secret_id, status, created_at, updated_at)
		VALUES('headscale', ?, ?, ?, 'configured', ?, ?)
		ON CONFLICT(kind) DO UPDATE SET mode = excluded.mode, endpoint = excluded.endpoint, secret_id = excluded.secret_id, status = 'configured', last_error = '', updated_at = excluded.updated_at`, input.Mode, endpoint, secretID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return IntegrationView{}, fmt.Errorf("center: save Headscale integration: %w", err)
	}
	if replacingSecret && existingSecretID != "" && existingSecretID != secretID {
		if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, existingSecretID); err != nil {
			return IntegrationView{}, fmt.Errorf("center: replace Headscale API key: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return IntegrationView{}, err
	}
	if input.Mode == "builtin" {
		if err := s.reconcileHeadscaleDNS(ctx); err != nil {
			return IntegrationView{}, err
		}
	}
	return s.Integration(ctx, "headscale")
}

func (s *Store) CreateHeadscaleJoin(ctx context.Context, agentID string) (HeadscaleJoin, error) {
	var capabilitiesJSON []byte
	if err := s.db.QueryRowContext(ctx, `SELECT capabilities_json FROM agents WHERE id = ? AND status = 'active'`, agentID).Scan(&capabilitiesJSON); errors.Is(err, sql.ErrNoRows) {
		return HeadscaleJoin{}, errors.New("center: Agent not found")
	} else if err != nil {
		return HeadscaleJoin{}, err
	}
	var capabilities NodeCapabilities
	if json.Unmarshal(capabilitiesJSON, &capabilities) != nil {
		return HeadscaleJoin{}, errors.New("center: Agent capabilities are invalid")
	}
	return s.createHeadscaleJoin(ctx, agentID, capabilities.Gateway)
}

func (s *Store) CreateHeadscaleBootstrap(ctx context.Context, gateway bool) (HeadscaleJoin, error) {
	return s.createHeadscaleJoin(ctx, "", gateway)
}

func (s *Store) createHeadscaleJoin(ctx context.Context, agentID string, gateway bool) (HeadscaleJoin, error) {
	client, err := s.headscale(ctx)
	if err != nil {
		return HeadscaleJoin{}, err
	}
	userID, err := client.ensureUser(ctx, "vastora")
	if err != nil {
		return HeadscaleJoin{}, err
	}
	expiresAt := s.now().UTC().Add(time.Hour)
	tags := []string{"tag:vastora-agent"}
	if gateway {
		tags = append(tags, "tag:vastora-gateway")
	}
	key, err := client.createPreAuthKey(ctx, userID, tags, expiresAt)
	if err != nil {
		return HeadscaleJoin{}, err
	}
	command := "sudo tailscale up --login-server " + shellQuote(client.baseURL) + " --auth-key " + shellQuote(key) + " --reset"
	return HeadscaleJoin{AgentID: agentID, Command: command, ExpiresAt: expiresAt}, nil
}

func (s *Store) headscale(ctx context.Context) (headscaleClient, error) {
	var endpoint, secretID string
	if err := s.db.QueryRowContext(ctx, `SELECT endpoint, secret_id FROM network_integrations WHERE kind = 'headscale' AND status = 'configured'`).Scan(&endpoint, &secretID); errors.Is(err, sql.ErrNoRows) {
		return headscaleClient{}, errors.New("center: Headscale integration is not configured")
	} else if err != nil {
		return headscaleClient{}, err
	}
	key, err := s.getSecret(ctx, secretID, "integration:headscale")
	if err != nil {
		return headscaleClient{}, err
	}
	allowedEndpoint, err := s.authorizedHeadscaleEndpoint(endpoint)
	if err != nil {
		return headscaleClient{}, err
	}
	return headscaleClient{baseURL: allowedEndpoint, apiKey: string(key), http: s.headscaleHTTPClient}, nil
}

func normalizeHeadscaleEndpoint(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || parsed.Path != "" || parsed.RawPath != "" {
		return "", errors.New("Headscale requires an HTTPS control-plane URL without a path")
	}
	return value, nil
}

func (s *Store) authorizedHeadscaleEndpoint(value string) (string, error) {
	requested, err := normalizeHeadscaleEndpoint(value)
	if err != nil {
		return "", fmt.Errorf("center: %w", err)
	}
	for _, allowed := range s.headscaleAllowedEndpoints {
		if requested == allowed {
			return allowed, nil
		}
	}
	return "", errors.New("center: Headscale URL is not allowed by this Center; add it with --headscale-allowed-url and restart Center")
}

func (s *Store) reconcileHeadscaleDNS(ctx context.Context) error {
	var mode string
	if err := s.db.QueryRowContext(ctx, `SELECT mode FROM network_integrations WHERE kind = 'headscale' AND status = 'configured'`).Scan(&mode); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: Headscale integration is not configured")
	} else if err != nil {
		return err
	}
	if mode != "builtin" {
		return errors.New("center: external Headscale DNS must be configured manually")
	}
	type record struct {
		Name  string `json:"name"`
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.hostname, n.headscale_address FROM publications p
		JOIN agent_network_profiles n ON n.agent_id = p.gateway_node_id
		WHERE p.kind = 'headscale_gateway' AND p.dns_provider = 'headscale' AND p.status <> 'stopped'
		ORDER BY p.hostname, p.id`)
	if err != nil {
		return err
	}
	records := []record{}
	for rows.Next() {
		var value record
		if err := rows.Scan(&value.Name, &value.Value); err != nil {
			rows.Close()
			return err
		}
		if strings.Contains(value.Value, ":") {
			value.Type = "AAAA"
		} else {
			value.Type = "A"
		}
		records = append(records, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Name == records[j].Name {
			return records[i].Value < records[j].Value
		}
		return records[i].Name < records[j].Name
	})
	payload, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	path := filepath.Join(s.dataDir, headscaleDNSFile)
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, payload, 0o644); err != nil {
		return fmt.Errorf("center: write Headscale DNS records: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("center: publish Headscale DNS records: %w", err)
	}
	return nil
}

func (s *Store) ensureHeadscaleDNSFile() error {
	path := filepath.Join(s.dataDir, headscaleDNSFile)
	if _, err := os.Stat(path); err == nil {
		return os.Chmod(path, 0o644)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("center: inspect Headscale DNS records: %w", err)
	}
	if err := os.WriteFile(path, []byte("[]\n"), 0o644); err != nil {
		return fmt.Errorf("center: initialize Headscale DNS records: %w", err)
	}
	return nil
}

func (client headscaleClient) verify(ctx context.Context) error {
	var result json.RawMessage
	return client.do(ctx, http.MethodGet, "/api/v1/user", nil, nil, &result)
}

func (client headscaleClient) ensureUser(ctx context.Context, name string) (string, error) {
	type user struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var users []user
	if err := client.do(ctx, http.MethodGet, "/api/v1/user", url.Values{"name": []string{name}}, nil, &users); err != nil {
		return "", err
	}
	for _, candidate := range users {
		if candidate.Name == name && candidate.ID != "" {
			return candidate.ID, nil
		}
	}
	var created user
	if err := client.do(ctx, http.MethodPost, "/api/v1/user", nil, map[string]string{"name": name}, &created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", errors.New("center: Headscale did not return the Vastora user ID")
	}
	return created.ID, nil
}

func (client headscaleClient) createPreAuthKey(ctx context.Context, user string, tags []string, expiration time.Time) (string, error) {
	body := map[string]any{"user": user, "reusable": false, "ephemeral": false, "expiration": expiration.Format(time.RFC3339), "aclTags": tags}
	var raw json.RawMessage
	if err := client.do(ctx, http.MethodPost, "/api/v1/preauthkey", nil, body, &raw); err != nil {
		return "", fmt.Errorf("center: create Headscale pre-auth key: %w", err)
	}
	var direct struct {
		Key        string `json:"key"`
		PreAuthKey struct {
			Key string `json:"key"`
		} `json:"preAuthKey"`
	}
	if json.Unmarshal(raw, &direct) != nil {
		return "", errors.New("center: Headscale returned an invalid pre-auth key")
	}
	key := direct.Key
	if key == "" {
		key = direct.PreAuthKey.Key
	}
	if strings.TrimSpace(key) == "" {
		return "", errors.New("center: Headscale did not return the one-time pre-auth key")
	}
	return key, nil
}

func (client headscaleClient) do(ctx context.Context, method, path string, query url.Values, body any, output any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	requestURL, err := headscaleRequestURL(client.baseURL, path, query)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, payload)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+client.apiKey)
	request.Header.Set("Content-Type", "application/json")
	httpClient := *client.http
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := strings.TrimSpace(string(raw))
		if message == "" {
			message = response.Status
		}
		return errors.New(message)
	}
	if output != nil && len(raw) != 0 {
		var envelope struct {
			User       json.RawMessage `json:"user"`
			Users      json.RawMessage `json:"users"`
			PreAuthKey json.RawMessage `json:"preAuthKey"`
		}
		if json.Unmarshal(raw, &envelope) == nil {
			switch {
			case len(envelope.PreAuthKey) != 0:
				raw = envelope.PreAuthKey
			case len(envelope.User) != 0:
				raw = envelope.User
			case len(envelope.Users) != 0:
				raw = envelope.Users
			}
		}
		if target, ok := output.(*json.RawMessage); ok {
			*target = append((*target)[:0], raw...)
		} else if err := json.Unmarshal(raw, output); err != nil {
			return err
		}
	}
	return nil
}

func headscaleRequestURL(baseURL, path string, query url.Values) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return "", errors.New("center: stored Headscale URL is invalid")
	}
	if !strings.HasPrefix(path, "/api/v1/") || strings.Contains(path, "?") || strings.Contains(path, "#") {
		return "", errors.New("center: Headscale API path is invalid")
	}
	target := *base
	target.Path = path
	target.RawPath = ""
	target.RawQuery = query.Encode()
	target.ForceQuery = false
	target.Fragment = ""
	return target.String(), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
