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
	"time"
)

type HeadscaleInstallRequest struct {
	CenterURL                string                `json:"centerUrl"`
	HeadscaleURL             string                `json:"headscaleUrl"`
	CenterAliases            []CenterEndpointAlias `json:"centerAliases,omitempty"`
	HeadscaleAliases         []string              `json:"headscaleAliases,omitempty"`
	PublicAddress            string                `json:"publicAddress,omitempty"`
	GatewayBindAddress       string                `json:"gatewayBindAddress,omitempty"`
	CenterPrivateBindAddress string                `json:"centerPrivateBindAddress,omitempty"`
	CenterCertificatePEM     string                `json:"centerCertificatePem"`
	CenterCertificateKeyPEM  string                `json:"centerCertificateKeyPem"`
}

type CenterEndpointAlias struct {
	URL               string `json:"url"`
	CertificatePEM    string `json:"certificatePem"`
	CertificateKeyPEM string `json:"certificateKeyPem"`
}

type HeadscaleInstallResult struct {
	Endpoint string `json:"endpoint"`
	APIKey   string `json:"apiKey"`
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
	if result.Endpoint == "" || len(result.APIKey) < 20 {
		return HeadscaleInstallResult{}, errors.New("center: deployment helper returned an incomplete Headscale result")
	}
	return result, nil
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
