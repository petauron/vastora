// Package deployapi defines the narrow local API used by Center to ask the
// privileged deployment helper to install bundled infrastructure. The helper
// is reachable only through a Unix socket; Center never receives Docker access.
package deployapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

const (
	HeadscaleDNSPolicySystem = "system"
	HeadscaleDNSPolicyCustom = "custom"
)

type HeadscaleInstallRequest struct {
	OperationID              string                `json:"operationId,omitempty"`
	CenterURL                string                `json:"centerUrl"`
	HeadscaleURL             string                `json:"headscaleUrl"`
	CenterAliases            []CenterEndpointAlias `json:"centerAliases,omitempty"`
	HeadscaleAliases         []string              `json:"headscaleAliases,omitempty"`
	PublicAddress            string                `json:"publicAddress,omitempty"`
	GatewayBindAddress       string                `json:"gatewayBindAddress,omitempty"`
	CenterPrivateBindAddress string                `json:"centerPrivateBindAddress,omitempty"`
	CenterCertificatePEM     string                `json:"centerCertificatePem"`
	CenterCertificateKeyPEM  string                `json:"centerCertificateKeyPem"`
	DNSPolicy                string                `json:"dnsPolicy"`
	DNSResolvers             []string              `json:"dnsResolvers,omitempty"`
}

func NormalizeHeadscaleDNS(policy string, resolvers []string) (string, []string, error) {
	policy = strings.TrimSpace(policy)
	if policy == "" {
		policy = HeadscaleDNSPolicySystem
	}
	if policy != HeadscaleDNSPolicySystem && policy != HeadscaleDNSPolicyCustom {
		return "", nil, errors.New("headscale DNS policy must be system or custom")
	}
	if policy == HeadscaleDNSPolicySystem {
		if len(resolvers) != 0 {
			return "", nil, errors.New("system Headscale DNS policy cannot include custom resolvers")
		}
		return policy, []string{}, nil
	}
	if len(resolvers) == 0 || len(resolvers) > 8 {
		return "", nil, errors.New("custom Headscale DNS policy requires one to eight resolvers")
	}
	normalized := make([]string, 0, len(resolvers))
	seen := make(map[string]struct{}, len(resolvers))
	for _, resolver := range resolvers {
		address, err := netip.ParseAddr(strings.TrimSpace(resolver))
		if err != nil || !address.IsValid() || address.Zone() != "" || address.IsUnspecified() || address.IsMulticast() {
			return "", nil, fmt.Errorf("headscale DNS resolver %q is not a valid unicast IP address", resolver)
		}
		value := address.Unmap().String()
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return "", nil, errors.New("custom Headscale DNS policy requires a resolver")
	}
	return policy, normalized, nil
}

type CenterEndpointAlias struct {
	URL               string `json:"url"`
	CertificatePEM    string `json:"certificatePem"`
	CertificateKeyPEM string `json:"certificateKeyPem"`
}

type HeadscaleInstallResult struct {
	Endpoint        string    `json:"endpoint"`
	APIKey          string    `json:"apiKey"`
	APIKeyID        uint64    `json:"apiKeyId"`
	APIKeyPrefix    string    `json:"apiKeyPrefix"`
	APIKeyExpiresAt time.Time `json:"apiKeyExpiresAt"`
}

type HeadscaleAPIKeyRotationRequest struct {
	CurrentPrefix string `json:"currentPrefix"`
}

type HeadscaleAPIKeyRotation struct {
	APIKey          string    `json:"apiKey"`
	APIKeyID        uint64    `json:"apiKeyId"`
	APIKeyPrefix    string    `json:"apiKeyPrefix"`
	APIKeyExpiresAt time.Time `json:"apiKeyExpiresAt"`
}

type HeadscaleAPIKeyCommitRequest struct {
	PreviousPrefix string `json:"previousPrefix"`
	CurrentPrefix  string `json:"currentPrefix"`
}

type HeadscaleInstallCommitRequest struct {
	OperationID string `json:"operationId"`
}

type HeadscaleInstallCommitter interface {
	CommitHeadscaleInstall(context.Context, HeadscaleInstallCommitRequest) error
}

type HeadscaleAPIKeyRotator interface {
	PrepareHeadscaleAPIKeyRotation(context.Context, HeadscaleAPIKeyRotationRequest) (HeadscaleAPIKeyRotation, error)
	CommitHeadscaleAPIKeyRotation(context.Context, HeadscaleAPIKeyCommitRequest) error
}

type HeadscaleInstaller interface {
	InstallHeadscale(context.Context, HeadscaleInstallRequest) (HeadscaleInstallResult, error)
	ReconcileHeadscale(context.Context, HeadscaleInstallRequest) error
}

type Client struct {
	http *http.Client
}

func NewClient(socket string) (*Client, error) {
	if socket == "" {
		return nil, errors.New("deployer: Unix socket path is required")
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	return &Client{http: &http.Client{Transport: transport, Timeout: 8 * time.Minute}}, nil
}

func (client *Client) InstallHeadscale(ctx context.Context, input HeadscaleInstallRequest) (HeadscaleInstallResult, error) {
	body, err := client.requestHeadscale(ctx, "/v1/headscale/install", input)
	if err != nil {
		return HeadscaleInstallResult{}, err
	}
	var result HeadscaleInstallResult
	if err := json.Unmarshal(body, &result); err != nil {
		return HeadscaleInstallResult{}, fmt.Errorf("center: decode deployment helper response: %w", err)
	}
	if result.Endpoint == "" || len(result.APIKey) < 20 || result.APIKeyID == 0 || result.APIKeyPrefix == "" || result.APIKeyExpiresAt.IsZero() {
		return HeadscaleInstallResult{}, errors.New("center: deployment helper returned an incomplete Headscale result")
	}
	return result, nil
}

func (client *Client) CommitHeadscaleInstall(ctx context.Context, input HeadscaleInstallCommitRequest) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	_, err = client.request(ctx, http.MethodPost, "/v1/headscale/install/commit", payload)
	return err
}

func (client *Client) PrepareHeadscaleAPIKeyRotation(ctx context.Context, input HeadscaleAPIKeyRotationRequest) (HeadscaleAPIKeyRotation, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return HeadscaleAPIKeyRotation{}, err
	}
	body, err := client.request(ctx, http.MethodPost, "/v1/headscale/api-key/prepare", payload)
	if err != nil {
		return HeadscaleAPIKeyRotation{}, err
	}
	var result HeadscaleAPIKeyRotation
	if err := json.Unmarshal(body, &result); err != nil {
		return HeadscaleAPIKeyRotation{}, fmt.Errorf("center: decode Headscale API key rotation: %w", err)
	}
	if len(result.APIKey) < 20 || result.APIKeyID == 0 || result.APIKeyPrefix == "" || result.APIKeyExpiresAt.IsZero() {
		return HeadscaleAPIKeyRotation{}, errors.New("center: deployment helper returned an incomplete Headscale API key rotation")
	}
	return result, nil
}

func (client *Client) CommitHeadscaleAPIKeyRotation(ctx context.Context, input HeadscaleAPIKeyCommitRequest) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	_, err = client.request(ctx, http.MethodPost, "/v1/headscale/api-key/commit", payload)
	return err
}

func (client *Client) ReconcileHeadscale(ctx context.Context, input HeadscaleInstallRequest) error {
	_, err := client.requestHeadscale(ctx, "/v1/headscale/reconcile", input)
	return err
}

func (client *Client) requestHeadscale(ctx context.Context, path string, input HeadscaleInstallRequest) ([]byte, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	return client.request(ctx, http.MethodPost, path, payload)
}
