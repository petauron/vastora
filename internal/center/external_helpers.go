package center

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

const (
	cloudflareAuthorizationURL = "https://dash.cloudflare.com/oauth2/auth"
	cloudflareTokenURL         = "https://dash.cloudflare.com/oauth2/token"
)

type ExternalHelperConfig struct {
	ReleaseMetadataURL         string
	ReleaseInstallerBaseURL    string
	PublicHelperOrigin         string
	RegionLookupURL            string
	CloudflareOAuthClientID    string
	CloudflareOAuthRedirectURL string
	CloudflareOAuthRelayURL    string
	AllowPrivate               bool
}

type ExternalHelperRuntime struct {
	ReleaseChecker           CenterReleaseChecker
	ReleaseInstallerBaseURL  string
	ResolveReleaseInstaller  func(context.Context) (ExternalHelperPin, error)
	PublicAddressLookupURL   string
	PublicHelperAllowPrivate bool
}

type ExternalHelperPin struct {
	Host    string
	Port    string
	Address string
}

func (s *Store) ConfigureExternalHelpers(config ExternalHelperConfig) (ExternalHelperRuntime, error) {
	var runtime ExternalHelperRuntime
	releaseConfigured := strings.TrimSpace(config.ReleaseMetadataURL) != "" || strings.TrimSpace(config.ReleaseInstallerBaseURL) != ""
	if releaseConfigured {
		if strings.TrimSpace(config.ReleaseMetadataURL) == "" || strings.TrimSpace(config.ReleaseInstallerBaseURL) == "" {
			return runtime, errors.New("center: release metadata URL and installer base URL must be configured together")
		}
		metadataURL, err := normalizeExternalHelperURL(config.ReleaseMetadataURL, false)
		if err != nil {
			return runtime, fmt.Errorf("center: release metadata helper: %w", err)
		}
		installerBaseURL, err := normalizeExternalHelperURL(config.ReleaseInstallerBaseURL, true)
		if err != nil {
			return runtime, fmt.Errorf("center: release installer base: %w", err)
		}
		client, err := externalHelperHTTPClient([]string{metadataURL}, config.AllowPrivate, 15*time.Second)
		if err != nil {
			return runtime, err
		}
		runtime.ReleaseChecker = NewReleaseChecker(metadataURL, client)
		runtime.ReleaseInstallerBaseURL = installerBaseURL
		runtime.ResolveReleaseInstaller = func(ctx context.Context) (ExternalHelperPin, error) {
			return resolveExternalHelperPin(ctx, installerBaseURL, config.AllowPrivate)
		}
	}

	if strings.TrimSpace(config.PublicHelperOrigin) != "" {
		origin, err := normalizeExternalHelperURL(config.PublicHelperOrigin, true)
		if err != nil {
			return runtime, fmt.Errorf("center: public network helper: %w", err)
		}
		parsed, _ := url.Parse(origin)
		if parsed.Path != "" {
			return runtime, errors.New("center: public network helper must be an origin without a path")
		}
		client, err := externalHelperHTTPClient([]string{origin}, config.AllowPrivate, 12*time.Second)
		if err != nil {
			return runtime, err
		}
		runtime.PublicAddressLookupURL = origin + "/network/public-address"
		runtime.PublicHelperAllowPrivate = config.AllowPrivate
		s.lookupPublicAddress = publicAddressLookup(client, runtime.PublicAddressLookupURL)
		s.verifyPublicEntry = publicEntryVerifier(client, origin+"/network/verify-public-entry")
	}

	if strings.TrimSpace(config.RegionLookupURL) != "" {
		regionURL, err := normalizeExternalHelperURL(config.RegionLookupURL, true)
		if err != nil {
			return runtime, fmt.Errorf("center: region lookup helper: %w", err)
		}
		client, err := externalHelperHTTPClient([]string{regionURL}, config.AllowPrivate, 8*time.Second)
		if err != nil {
			return runtime, err
		}
		s.lookupPublicRegion = regionLookupAt(client, regionURL)
	}

	oauthValues := []string{config.CloudflareOAuthClientID, config.CloudflareOAuthRedirectURL, config.CloudflareOAuthRelayURL}
	oauthConfigured := false
	for _, value := range oauthValues {
		oauthConfigured = oauthConfigured || strings.TrimSpace(value) != ""
	}
	if oauthConfigured {
		for _, value := range oauthValues {
			if strings.TrimSpace(value) == "" {
				return runtime, errors.New("center: Cloudflare OAuth client ID, redirect URL, and relay URL must be configured together")
			}
		}
		clientID := strings.TrimSpace(config.CloudflareOAuthClientID)
		if len(clientID) > 256 || strings.ContainsAny(clientID, "\r\n\t ") {
			return runtime, errors.New("center: Cloudflare OAuth client ID is invalid")
		}
		redirectURL, err := normalizeExternalHelperURL(config.CloudflareOAuthRedirectURL, false)
		if err != nil {
			return runtime, fmt.Errorf("center: Cloudflare OAuth redirect: %w", err)
		}
		relayURL, err := normalizeExternalHelperURL(config.CloudflareOAuthRelayURL, true)
		if err != nil {
			return runtime, fmt.Errorf("center: Cloudflare OAuth relay: %w", err)
		}
		client, err := externalHelperHTTPClient([]string{relayURL, cloudflareTokenURL, cloudflareAPIURL}, config.AllowPrivate, 20*time.Second)
		if err != nil {
			return runtime, err
		}
		s.cloudflareOAuth = cloudflareOAuthConfig{
			ClientID:         clientID,
			RedirectURL:      redirectURL,
			AuthorizationURL: cloudflareAuthorizationURL,
			TokenURL:         cloudflareTokenURL,
			RelayURL:         relayURL,
			APIURL:           cloudflareAPIURL,
			HTTPClient:       client,
		}
	}
	return runtime, nil
}

func resolveExternalHelperPin(ctx context.Context, endpoint string, allowPrivate bool) (ExternalHelperPin, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" {
		return ExternalHelperPin{}, errors.New("center: release installer base is invalid")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, parsed.Hostname())
	if err != nil || len(addresses) == 0 {
		return ExternalHelperPin{}, errors.New("center: release installer hostname could not be resolved")
	}
	for _, candidate := range addresses {
		if err := validateExternalHelperIP(candidate.IP, allowPrivate); err != nil {
			return ExternalHelperPin{}, err
		}
	}
	selected := addresses[0].IP
	for _, candidate := range addresses {
		if candidate.IP.To4() != nil {
			selected = candidate.IP
			break
		}
	}
	port := parsed.Port()
	if port == "" {
		port = "443"
	}
	return ExternalHelperPin{Host: parsed.Hostname(), Port: port, Address: selected.String()}, nil
}

func normalizeExternalHelperURL(raw string, trimPath bool) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Scheme == "" {
		return "", errors.New("helper URL must be an exact credential-free URL")
	}
	if parsed.Scheme != "https" {
		return "", errors.New("helper URL requires HTTPS")
	}
	if parsed.Hostname() == "" {
		return "", errors.New("helper URL requires a hostname")
	}
	if trimPath {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
	}
	parsed.RawPath = ""
	return parsed.String(), nil
}

func externalHelperHTTPClient(endpoints []string, allowPrivate bool, timeout time.Duration) (*http.Client, error) {
	allowedHosts := make(map[string]struct{}, len(endpoints))
	for _, endpoint := range endpoints {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Hostname() == "" {
			return nil, errors.New("center: external helper endpoint is invalid")
		}
		allowedHosts[strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))] = struct{}{}
	}
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	transport := &http.Transport{ForceAttemptHTTP2: true, Proxy: nil, TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: timeout}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("center: external helper attempted an invalid connection")
		}
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		if _, ok := allowedHosts[host]; !ok {
			return nil, errors.New("center: external helper attempted an unexpected connection")
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(addresses) == 0 {
			return nil, errors.New("center: external helper hostname could not be resolved")
		}
		for _, candidate := range addresses {
			if err := validateExternalHelperIP(candidate.IP, allowPrivate); err != nil {
				return nil, err
			}
		}
		selected := addresses[0].IP
		for _, candidate := range addresses {
			if candidate.IP.To4() != nil {
				selected = candidate.IP
				break
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(selected.String(), port))
	}
	return &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func validateExternalHelperIP(ip net.IP, allowPrivate bool) error {
	return networking.ValidateExternalHelperAddress(ip, allowPrivate)
}
