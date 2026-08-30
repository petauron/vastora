package catalog

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const MaxEnvelopeBytes int64 = 5 << 20

type FetchConfig struct {
	URL          string
	PublicKey    ed25519.PublicKey
	BearerToken  string
	CustomCAPEM  []byte
	ETag         string
	LastModified string
	Timeout      time.Duration
}

type FetchResult struct {
	Envelope     Envelope
	Catalog      Catalog
	RawEnvelope  []byte
	ETag         string
	LastModified string
	NotModified  bool
}

// Fetch verifies the remote envelope before returning it. It intentionally
// permits only HTTPS, bounds the response, and strips credentials on an
// cross-origin redirect.
func Fetch(ctx context.Context, config FetchConfig) (FetchResult, error) {
	parsedURL, err := url.Parse(config.URL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" || parsedURL.User != nil {
		return FetchResult{}, errors.New("catalog: source URL must be absolute HTTPS without credentials")
	}
	if config.Timeout <= 0 {
		config.Timeout = 10 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if len(config.CustomCAPEM) > 0 {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(config.CustomCAPEM) {
			return FetchResult{}, errors.New("catalog: custom CA is not valid PEM")
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   config.Timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("catalog: too many redirects")
			}
			if request.URL == nil || request.URL.Scheme != "https" || request.URL.Host == "" || request.URL.User != nil {
				return errors.New("catalog: redirect target must be absolute HTTPS without credentials")
			}
			if len(via) > 0 && !sameOrigin(request.URL, via[len(via)-1].URL) {
				request.Header.Del("Authorization")
			}
			return nil
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, config.URL, nil)
	if err != nil {
		return FetchResult{}, fmt.Errorf("catalog: create request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if config.ETag != "" {
		request.Header.Set("If-None-Match", config.ETag)
	}
	if config.LastModified != "" {
		request.Header.Set("If-Modified-Since", config.LastModified)
	}
	if config.BearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+config.BearerToken)
	}
	response, err := client.Do(request)
	if err != nil {
		return FetchResult{}, fmt.Errorf("catalog: fetch source: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		if config.ETag == "" && config.LastModified == "" {
			return FetchResult{}, errors.New("catalog: source returned 304 without a conditional request")
		}
		etag := strings.TrimSpace(response.Header.Get("ETag"))
		if etag == "" {
			etag = config.ETag
		}
		lastModified := strings.TrimSpace(response.Header.Get("Last-Modified"))
		if lastModified == "" {
			lastModified = config.LastModified
		}
		return FetchResult{NotModified: true, ETag: etag, LastModified: lastModified}, nil
	}
	if response.StatusCode != http.StatusOK {
		return FetchResult{}, fmt.Errorf("catalog: source returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > MaxEnvelopeBytes {
		return FetchResult{}, errors.New("catalog: source exceeds maximum size")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxEnvelopeBytes+1))
	if err != nil {
		return FetchResult{}, fmt.Errorf("catalog: read source: %w", err)
	}
	if int64(len(raw)) > MaxEnvelopeBytes {
		return FetchResult{}, errors.New("catalog: source exceeds maximum size")
	}
	envelope, err := ParseEnvelope(raw)
	if err != nil {
		return FetchResult{}, err
	}
	parsedCatalog, _, err := Verify(envelope, config.PublicKey)
	if err != nil {
		return FetchResult{}, err
	}
	return FetchResult{
		Envelope:     envelope,
		Catalog:      parsedCatalog,
		RawEnvelope:  raw,
		ETag:         strings.TrimSpace(response.Header.Get("ETag")),
		LastModified: strings.TrimSpace(response.Header.Get("Last-Modified")),
	}, nil
}

func sameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}
