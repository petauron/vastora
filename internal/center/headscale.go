package center

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/networking"
)

const (
	headscaleDNSFile               = "headscale-extra-records.json"
	builtinHeadscaleRuntimeSetting = "builtin_headscale_runtime"
	builtinHeadscaleRuntimeVersion = "dns-policy-v3"
	headscaleDNSPolicySetting      = "headscale_dns_policy"
	headscaleDNSResolversSetting   = "headscale_dns_resolvers"
	headscalePinnedRequestOrigin   = "https://headscale-api.vastora.invalid"
)

type HeadscaleInput struct {
	Mode         string   `json:"mode"`
	URL          string   `json:"url"`
	APIKey       string   `json:"apiKey"`
	DNSPolicy    string   `json:"dnsPolicy,omitempty"`
	DNSResolvers []string `json:"dnsResolvers,omitempty"`
}

type HeadscaleJoin struct {
	AgentID   string    `json:"agentId,omitempty"`
	Command   string    `json:"command"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type TailscaleIsolationDesiredState struct {
	ControlURL        string   `json:"controlUrl"`
	ControlAddresses  []string `json:"controlAddresses"`
	ControlAliases    []string `json:"controlAliases,omitempty"`
	StaticEndpoints   []string `json:"staticEndpoints"`
	RelayRegionID     int      `json:"relayRegionId,omitempty"`
	STUNOnlyRegionIDs []int    `json:"stunOnlyRegionIds,omitempty"`
}

type headscaleClient struct {
	baseURL   string
	authority string
	apiKey    string
	http      *http.Client
}

func (s *Store) ConfigureHeadscale(ctx context.Context, input HeadscaleInput) (IntegrationView, error) {
	return s.configureHeadscale(ctx, input, false, nil)
}

func (s *Store) ConfigureBuiltinHeadscale(ctx context.Context, result deployapi.HeadscaleInstallResult, dnsPolicy string, dnsResolvers []string) (IntegrationView, error) {
	if result.APIKeyID == 0 || strings.TrimSpace(result.APIKeyPrefix) == "" || !result.APIKeyExpiresAt.After(s.now().UTC()) {
		return IntegrationView{}, errors.New("center: deployment helper returned invalid Headscale API key metadata")
	}
	return s.configureHeadscale(ctx, HeadscaleInput{Mode: "builtin", URL: result.Endpoint, APIKey: result.APIKey, DNSPolicy: dnsPolicy, DNSResolvers: dnsResolvers}, true, &result)
}

func (s *Store) builtinHeadscaleDNSConfig(ctx context.Context) (string, []string, error) {
	var policy, encodedResolvers string
	err := s.db.QueryRowContext(ctx, `SELECT policy.value, resolvers.value FROM settings policy, settings resolvers
		WHERE policy.key = ? AND resolvers.key = ?`, headscaleDNSPolicySetting, headscaleDNSResolversSetting).Scan(&policy, &encodedResolvers)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, errors.New("center: built-in Headscale DNS policy is not configured")
	}
	if err != nil {
		return "", nil, fmt.Errorf("center: read built-in Headscale DNS policy: %w", err)
	}
	var resolvers []string
	if json.Unmarshal([]byte(encodedResolvers), &resolvers) != nil {
		return "", nil, errors.New("center: stored Headscale DNS resolvers are invalid")
	}
	policy, resolvers, err = deployapi.NormalizeHeadscaleDNS(policy, resolvers)
	if err != nil {
		return "", nil, fmt.Errorf("center: stored Headscale DNS policy: %w", err)
	}
	return policy, resolvers, nil
}

func (s *Store) builtinHeadscaleRuntime(ctx context.Context) (string, string, bool, error) {
	var endpoint, runtime string
	err := s.db.QueryRowContext(ctx, `SELECT integrations.endpoint, COALESCE(settings.value, '')
		FROM network_integrations integrations
		LEFT JOIN settings ON settings.key = ?
		WHERE integrations.kind = 'headscale' AND integrations.mode = 'builtin' AND integrations.status = 'configured'`, builtinHeadscaleRuntimeSetting).Scan(&endpoint, &runtime)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("center: read built-in Headscale runtime: %w", err)
	}
	return endpoint, runtime, true, nil
}

func (s *Store) markBuiltinHeadscaleRuntime(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, builtinHeadscaleRuntimeSetting, builtinHeadscaleRuntimeVersion); err != nil {
		return fmt.Errorf("center: save built-in Headscale runtime: %w", err)
	}
	return nil
}

func (s *Store) configureHeadscale(ctx context.Context, input HeadscaleInput, trustedBuiltin bool, builtinKey *deployapi.HeadscaleInstallResult) (IntegrationView, error) {
	var err error
	input.Mode = strings.TrimSpace(input.Mode)
	input.APIKey = strings.TrimSpace(input.APIKey)
	if input.Mode != "builtin" && input.Mode != "external" {
		return IntegrationView{}, errors.New("center: Headscale mode must be builtin or external")
	}
	if trustedBuiltin != (input.Mode == "builtin") {
		return IntegrationView{}, errors.New("center: built-in Headscale must be installed by the deployment helper")
	}
	if trustedBuiltin {
		input.DNSPolicy, input.DNSResolvers, err = deployapi.NormalizeHeadscaleDNS(input.DNSPolicy, input.DNSResolvers)
		if err != nil {
			return IntegrationView{}, fmt.Errorf("center: Headscale DNS: %w", err)
		}
	} else if strings.TrimSpace(input.DNSPolicy) != "" || len(input.DNSResolvers) != 0 {
		return IntegrationView{}, errors.New("center: DNS policy is managed only for built-in Headscale")
	}
	var endpoint string
	if trustedBuiltin {
		endpoint, err = normalizeHeadscaleEndpoint(input.URL)
	} else {
		endpoint, err = s.authorizedHeadscaleEndpoint(input.URL)
	}
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
	dialAddress := ""
	if trustedBuiltin {
		dialAddress = s.builtinHeadscaleDialAddress
	}
	client, err := newHeadscaleClient(endpoint, input.APIKey, dialAddress, s.headscaleHTTPClient)
	if err != nil {
		return IntegrationView{}, err
	}
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
	if trustedBuiltin {
		if _, err := tx.ExecContext(ctx, `INSERT INTO headscale_api_keys(id, key_id, key_prefix, expires_at, state, previous_prefix, last_error, updated_at)
			VALUES(1, ?, ?, ?, 'ready', '', '', ?)
			ON CONFLICT(id) DO UPDATE SET key_id = excluded.key_id, key_prefix = excluded.key_prefix, expires_at = excluded.expires_at,
			state = 'ready', previous_prefix = '', last_error = '', updated_at = excluded.updated_at`, builtinKey.APIKeyID, builtinKey.APIKeyPrefix, builtinKey.APIKeyExpiresAt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			return IntegrationView{}, fmt.Errorf("center: save Headscale API key lifecycle: %w", err)
		}
		encodedResolvers, err := json.Marshal(input.DNSResolvers)
		if err != nil {
			return IntegrationView{}, err
		}
		for key, value := range map[string]string{headscaleDNSPolicySetting: input.DNSPolicy, headscaleDNSResolversSetting: string(encodedResolvers)} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?)
				ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
				return IntegrationView{}, fmt.Errorf("center: save Headscale DNS policy: %w", err)
			}
		}
	} else if _, err := tx.ExecContext(ctx, `DELETE FROM headscale_api_keys`); err != nil {
		return IntegrationView{}, fmt.Errorf("center: disable bundled Headscale API key lifecycle: %w", err)
	} else if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key IN (?, ?, ?)`, builtinHeadscaleRuntimeSetting, headscaleDNSPolicySetting, headscaleDNSResolversSetting); err != nil {
		return IntegrationView{}, fmt.Errorf("center: disable bundled Headscale settings: %w", err)
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
	} else if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, tailscaleFixedEndpointSetting); err != nil {
		return IntegrationView{}, fmt.Errorf("center: disable managed Tailscale fixed endpoint: %w", err)
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
	// login creates a separate switchable profile. Reusing `up --reset` would
	// overwrite the active profile and make an interrupted Center migration
	// impossible to roll back without authenticating again.
	command := "sudo tailscale login --login-server " + shellQuote(client.baseURL) + " --auth-key " + shellQuote(key)
	return HeadscaleJoin{AgentID: agentID, Command: command, ExpiresAt: expiresAt}, nil
}

func (s *Store) headscale(ctx context.Context) (headscaleClient, error) {
	var mode, endpoint, secretID string
	if err := s.db.QueryRowContext(ctx, `SELECT mode, endpoint, secret_id FROM network_integrations WHERE kind = 'headscale' AND status = 'configured'`).Scan(&mode, &endpoint, &secretID); errors.Is(err, sql.ErrNoRows) {
		return headscaleClient{}, errors.New("center: Headscale integration is not configured")
	} else if err != nil {
		return headscaleClient{}, err
	}
	key, err := s.getSecret(ctx, secretID, "integration:headscale")
	if err != nil {
		return headscaleClient{}, err
	}
	allowedEndpoint := ""
	if mode == "builtin" {
		allowedEndpoint, err = normalizeHeadscaleEndpoint(endpoint)
	} else {
		allowedEndpoint, err = s.authorizedHeadscaleEndpoint(endpoint)
	}
	if err != nil {
		return headscaleClient{}, err
	}
	dialAddress := ""
	if mode == "builtin" {
		dialAddress = s.builtinHeadscaleDialAddress
	}
	return newHeadscaleClient(allowedEndpoint, string(key), dialAddress, s.headscaleHTTPClient)
}

func (s *Store) tailscaleIsolationDesiredState(ctx context.Context, agentID string) (*TailscaleIsolationDesiredState, error) {
	var mode, endpoint string
	if err := s.db.QueryRowContext(ctx, `SELECT mode, endpoint FROM network_integrations WHERE kind = 'headscale' AND status = 'configured'`).Scan(&mode, &endpoint); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("center: read Tailscale isolation state: %w", err)
	}
	normalized, err := normalizeHeadscaleEndpoint(endpoint)
	if err != nil {
		return nil, fmt.Errorf("center: stored Headscale endpoint is invalid: %w", err)
	}
	state := &TailscaleIsolationDesiredState{ControlURL: normalized, StaticEndpoints: []string{}}
	if mode != "builtin" {
		return state, nil
	}
	state.RelayRegionID = 999
	state.STUNOnlyRegionIDs = []int{998}
	aliases, err := readActiveSystemEndpointAliases(ctx, s.db, "headscale")
	if err != nil {
		return nil, fmt.Errorf("center: read Headscale endpoint aliases: %w", err)
	}
	for _, alias := range aliases {
		if alias.Endpoint != normalized {
			state.ControlAliases = append(state.ControlAliases, alias.Endpoint)
		}
	}
	binding, configured, err := s.setupGatewayBinding(ctx)
	if err != nil {
		return nil, fmt.Errorf("center: read Headscale public binding: %w", err)
	}
	if !configured {
		return state, nil
	}
	publicIP := net.ParseIP(binding.PublicAddress)
	if publicIP == nil {
		return nil, errors.New("center: built-in Headscale public address is invalid")
	}
	controlAddress := publicIP.String()
	if strings.TrimSpace(agentID) != "" && binding.BindAddress != binding.PublicAddress {
		var local int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_network_candidates WHERE agent_id = ? AND address = ?`, agentID, binding.BindAddress).Scan(&local); err != nil {
			return nil, fmt.Errorf("center: inspect co-located Headscale Agent: %w", err)
		}
		if local > 0 {
			controlAddress = net.ParseIP(binding.BindAddress).String()
		}
	}
	state.ControlAddresses = []string{controlAddress}
	fixedEndpoint, err := s.readTailscaleFixedEndpoint(ctx)
	if err != nil {
		return nil, fmt.Errorf("center: read Tailscale fixed endpoint: %w", err)
	}
	if !fixedEndpoint.Enabled {
		return state, nil
	}
	owner, err := s.coLocatedTailscaleEndpointOwner(ctx, fixedEndpoint.LocalAddress)
	if err != nil {
		return nil, err
	}
	if owner == nil || owner.ID != strings.TrimSpace(agentID) || owner.Ownership != "managed" {
		return state, nil
	}
	current, _, err := s.tailscaleFixedEndpointCurrent(ctx, fixedEndpoint)
	if err != nil {
		return nil, err
	}
	if current {
		state.StaticEndpoints = []string{fixedEndpoint.Endpoint}
	}
	return state, nil
}

type tailscaleEndpointOwner struct {
	ID        string
	Ownership string
}

func (s *Store) coLocatedTailscaleEndpointOwner(ctx context.Context, bindAddress string) (*tailscaleEndpointOwner, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT a.id, a.tailscale_ownership
		FROM agents a JOIN agent_network_candidates c ON c.agent_id = a.id
		WHERE a.status = 'active' AND a.last_seen_at >= ? AND c.address = ?
		ORDER BY a.id`, s.now().UTC().Add(-45*time.Second).Format(time.RFC3339Nano), bindAddress)
	if err != nil {
		return nil, fmt.Errorf("center: inspect co-located Tailscale endpoint owner: %w", err)
	}
	defer rows.Close()
	owners := make([]tailscaleEndpointOwner, 0, 2)
	for rows.Next() {
		var owner tailscaleEndpointOwner
		if err := rows.Scan(&owner.ID, &owner.Ownership); err != nil {
			return nil, fmt.Errorf("center: inspect co-located Tailscale endpoint owner: %w", err)
		}
		owners = append(owners, owner)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("center: inspect co-located Tailscale endpoint owner: %w", err)
	}
	if len(owners) != 1 {
		return nil, nil
	}
	return &owners[0], nil
}

func newHeadscaleClient(endpoint, apiKey, dialAddress string, base *http.Client) (headscaleClient, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || parsed.Path != "" || parsed.RawPath != "" {
		return headscaleClient{}, errors.New("center: Headscale URL is invalid")
	}
	if dialAddress == "" {
		port := parsed.Port()
		if port == "" {
			port = "443"
		}
		dialAddress = net.JoinHostPort(parsed.Hostname(), port)
	}
	dialHost, dialPort, err := net.SplitHostPort(dialAddress)
	if err != nil || strings.TrimSpace(dialHost) == "" {
		return headscaleClient{}, errors.New("center: Headscale dial address is invalid")
	}
	if port, err := strconv.Atoi(dialPort); err != nil || port < 1 || port > 65535 {
		return headscaleClient{}, errors.New("center: Headscale dial address is invalid")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if configured, ok := base.Transport.(*http.Transport); ok {
		transport = configured.Clone()
	}
	transport.Proxy = nil
	// Custom TLS dial hooks bypass DialContext. Do not inherit a second route
	// around the fixed destination when cloning the configured transport.
	transport.DialTLSContext = nil
	transport.DialTLS = nil
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", dialAddress)
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if transport.TLSClientConfig != nil {
		tlsConfig = transport.TLSClientConfig.Clone()
		if tlsConfig.MinVersion < tls.VersionTLS12 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}
	}
	tlsConfig.ServerName = parsed.Hostname()
	transport.TLSClientConfig = tlsConfig
	client := *base
	client.Transport = transport
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return headscaleClient{baseURL: endpoint, authority: parsed.Host, apiKey: apiKey, http: &client}, nil
}

func normalizeHeadscaleEndpoint(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || parsed.Path != "" || parsed.RawPath != "" {
		return "", errors.New("headscale requires an HTTPS control-plane URL without a path")
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
	return s.reconcileHeadscaleDNSForSystem(ctx, "", nil)
}

func (s *Store) reconcileHeadscaleDNSForSystem(ctx context.Context, primaryCenterEndpoint string, additionalCenterEndpoints []string) error {
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
	records := []record{}
	var connectionMode, centerEndpoint string
	modeErr := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, agentConnectionModeSetting).Scan(&connectionMode)
	endpointErr := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, agentConnectURLSetting).Scan(&centerEndpoint)
	if modeErr == nil && endpointErr == nil && connectionMode == "headscale" {
		if strings.TrimSpace(primaryCenterEndpoint) != "" {
			centerEndpoint = primaryCenterEndpoint
		}
		if centerAddress, err := s.coLocatedHeadscaleAddress(ctx); err != nil {
			return err
		} else if centerAddress != "" {
			if ip := net.ParseIP(centerAddress); ip == nil || ip.To4() == nil {
				return errors.New("center: Headscale DNS address must be IPv4")
			}
			endpoints := []string{centerEndpoint}
			aliases, err := readActiveSystemEndpointAliases(ctx, s.db, "center")
			if err != nil {
				return err
			}
			for _, alias := range aliases {
				endpoints = append(endpoints, alias.Endpoint)
			}
			endpoints = append(endpoints, additionalCenterEndpoints...)
			for _, endpoint := range endpoints {
				hostname, err := gatewayEndpointHostname(endpoint)
				if err != nil {
					return err
				}
				records = append(records, record{Name: hostname, Type: "A", Value: centerAddress})
			}
		}
	} else if (modeErr != nil && !errors.Is(modeErr, sql.ErrNoRows)) || (endpointErr != nil && !errors.Is(endpointErr, sql.ErrNoRows)) {
		return errors.Join(modeErr, endpointErr)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.hostname, n.headscale_address FROM publications p
		JOIN agent_network_profiles n ON n.agent_id = p.gateway_node_id
		WHERE p.kind = 'headscale_gateway' AND p.dns_provider = 'headscale' AND p.status <> 'stopped'
		ORDER BY p.hostname, p.id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var value record
		if err := rows.Scan(&value.Name, &value.Value); err != nil {
			rows.Close()
			return err
		}
		if ip := net.ParseIP(value.Value); ip == nil || ip.To4() == nil {
			rows.Close()
			return errors.New("center: Headscale publication address must be IPv4")
		}
		value.Type = "A"
		records = append(records, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	unique := make([]record, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, value := range records {
		key := value.Name + "\x00" + value.Type + "\x00" + value.Value
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, value)
	}
	records = unique
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

func (s *Store) coLocatedHeadscaleAddress(ctx context.Context) (string, error) {
	localCandidates, err := s.discoverNetworkCandidates(s.now().UTC())
	if err != nil {
		return "", fmt.Errorf("center: discover control-plane addresses: %w", err)
	}
	local := make(map[string]string, len(localCandidates))
	for _, candidate := range localCandidates {
		if candidate.Kind == networking.KindLAN || candidate.Kind == networking.KindPublic || candidate.Kind == networking.KindHeadscale {
			local[candidate.Kind+"\x00"+candidate.Address] = candidate.Address
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT n.headscale_address, c.kind, c.address
		FROM agent_network_profiles n JOIN agents a ON a.id = n.agent_id
		JOIN agent_network_candidates c ON c.agent_id = n.agent_id
		WHERE a.status = 'active' AND a.last_seen_at >= ? AND n.headscale_address <> ''
		AND EXISTS(SELECT 1 FROM agent_network_candidates h WHERE h.agent_id = n.agent_id AND h.kind = ? AND h.address = n.headscale_address)
		ORDER BY a.enrolled_at, c.kind, c.address`, s.now().UTC().Add(-45*time.Second).Format(time.RFC3339Nano), networking.KindHeadscale)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var headscaleAddress, kind, address string
		if err := rows.Scan(&headscaleAddress, &kind, &address); err != nil {
			return "", err
		}
		if _, coLocated := local[kind+"\x00"+address]; coLocated {
			return headscaleAddress, nil
		}
	}
	return "", rows.Err()
}

func (s *Store) networkCandidatesAreCoLocated(candidates []networking.Candidate) (bool, error) {
	host, err := s.discoverNetworkCandidates(s.now().UTC())
	if err != nil {
		return false, fmt.Errorf("center: discover co-located Agent addresses: %w", err)
	}
	hostAddresses := make(map[string]struct{}, len(host))
	for _, candidate := range host {
		if candidate.Kind == networking.KindLAN || candidate.Kind == networking.KindPublic {
			hostAddresses[candidate.Kind+"\x00"+candidate.Address] = struct{}{}
		}
	}
	for _, candidate := range candidates {
		if _, exists := hostAddresses[candidate.Kind+"\x00"+candidate.Address]; exists {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) reconcileBuiltinHeadscaleDNSIfConfigured(ctx context.Context) error {
	var configured int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM network_integrations WHERE kind = 'headscale' AND mode = 'builtin' AND status = 'configured'`).Scan(&configured); err != nil {
		return err
	}
	if configured == 0 {
		return nil
	}
	return s.reconcileHeadscaleDNS(ctx)
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
	requestURL, err := headscaleRequestURL(path, query)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, payload)
	if err != nil {
		return err
	}
	request.Host = client.authority
	request.Header.Set("Authorization", "Bearer "+client.apiKey)
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

func headscaleRequestURL(path string, query url.Values) (string, error) {
	if !strings.HasPrefix(path, "/api/v1/") || strings.Contains(path, "?") || strings.Contains(path, "#") {
		return "", errors.New("center: Headscale API path is invalid")
	}
	target, err := url.Parse(headscalePinnedRequestOrigin)
	if err != nil {
		return "", errors.New("center: pinned Headscale request origin is invalid")
	}
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
