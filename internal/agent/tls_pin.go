package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func normalizeCAFingerprint(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validateCAFingerprint(centerURL, value string) error {
	parsed, err := url.Parse(strings.TrimSpace(centerURL))
	if err != nil {
		return errors.New("agent: invalid Center URL")
	}
	value = normalizeCAFingerprint(value)
	if parsed.Scheme == "http" && loopbackCenterURL(centerURL) && value == "" {
		return nil
	}
	decoded, err := hex.DecodeString(value)
	if parsed.Scheme != "https" || err != nil || len(decoded) != sha256.Size {
		return errors.New("agent: Center CA fingerprint is invalid; enroll the Agent again")
	}
	return nil
}

func normalizeCenterTrust(centerURL, fingerprint, certificatePEM string) (string, string, error) {
	fingerprint = normalizeCAFingerprint(fingerprint)
	certificatePEM = strings.TrimSpace(certificatePEM)
	if certificatePEM != "" {
		certificate, err := parseCACertificate(certificatePEM)
		if err != nil {
			return "", "", err
		}
		certificateFingerprint := certificatePublicKeyFingerprint(certificate)
		if fingerprint != "" && fingerprint != certificateFingerprint {
			return "", "", errors.New("agent: Center CA certificate does not match its expected fingerprint")
		}
		fingerprint = certificateFingerprint
		certificatePEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}))
	}
	if fingerprint == "" && certificatePEM == "" && !loopbackCenterURL(centerURL) {
		return "", "", nil
	}
	if err := validateCAFingerprint(centerURL, fingerprint); err != nil {
		return "", "", err
	}
	return fingerprint, certificatePEM, nil
}

func parseCACertificate(value string) (*x509.Certificate, error) {
	block, rest := pem.Decode([]byte(strings.TrimSpace(value)))
	if block == nil || block.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
		return nil, errors.New("agent: Center CA certificate must contain exactly one PEM certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !certificate.IsCA || !certificate.BasicConstraintsValid {
		return nil, errors.New("agent: Center CA certificate is invalid")
	}
	return certificate, nil
}

func (c Client) probeCenterCAFingerprint(ctx context.Context, centerURL string) (string, error) {
	if loopbackCenterURL(centerURL) {
		return "", nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(centerURL, "/")+"/healthz", nil)
	if err != nil {
		return "", fmt.Errorf("agent: create Center identity probe: %w", err)
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("agent: verify Center TLS identity: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 {
		return "", errors.New("agent: Center did not provide a verifiable TLS identity")
	}
	for index := len(response.TLS.PeerCertificates) - 1; index > 0; index-- {
		if response.TLS.PeerCertificates[index].IsCA {
			return certificatePublicKeyFingerprint(response.TLS.PeerCertificates[index]), nil
		}
	}
	root := response.TLS.VerifiedChains[0][len(response.TLS.VerifiedChains[0])-1]
	return certificatePublicKeyFingerprint(root), nil
}

func certificatePublicKeyFingerprint(certificate *x509.Certificate) string {
	digest := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(digest[:])
}

func pinnedHTTPClient(fingerprint, certificatePEM string, timeout time.Duration) (*http.Client, error) {
	fingerprint = normalizeCAFingerprint(fingerprint)
	if _, err := hex.DecodeString(fingerprint); err != nil || len(fingerprint) != sha256.Size*2 {
		return nil, errors.New("agent: invalid Center CA fingerprint")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.TrimSpace(certificatePEM) != "" {
		certificate, err := parseCACertificate(certificatePEM)
		if err != nil {
			return nil, err
		}
		if certificatePublicKeyFingerprint(certificate) != fingerprint {
			return nil, errors.New("agent: Center CA certificate does not match its expected fingerprint")
		}
		roots := x509.NewCertPool()
		roots.AddCert(certificate)
		transport.TLSClientConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
			VerifyConnection: func(state tls.ConnectionState) error {
				for _, chain := range state.VerifiedChains {
					if len(chain) != 0 && certificatePublicKeyFingerprint(chain[len(chain)-1]) == fingerprint {
						return nil
					}
				}
				return errors.New("agent: Center CA fingerprint mismatch")
			},
		}
		return &http.Client{Transport: transport, Timeout: timeout}, nil
	}
	transport.TLSClientConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // Verification is performed below against the pinned CA.
		VerifyConnection: func(state tls.ConnectionState) error {
			return verifyPinnedCA(state, fingerprint)
		},
	}
	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

func verifyPinnedCA(state tls.ConnectionState, fingerprint string) error {
	if len(state.PeerCertificates) == 0 {
		return errors.New("agent: Center did not provide a TLS certificate")
	}
	leaf := state.PeerCertificates[0]
	intermediates := x509.NewCertPool()
	for _, certificate := range state.PeerCertificates[1:] {
		intermediates.AddCert(certificate)
	}
	options := x509.VerifyOptions{DNSName: state.ServerName, Intermediates: intermediates}
	if chains, err := leaf.Verify(options); err == nil {
		for _, chain := range chains {
			if len(chain) != 0 && certificatePublicKeyFingerprint(chain[len(chain)-1]) == fingerprint {
				return nil
			}
		}
	}
	for _, certificate := range state.PeerCertificates[1:] {
		if !certificate.IsCA || certificatePublicKeyFingerprint(certificate) != fingerprint {
			continue
		}
		roots := x509.NewCertPool()
		roots.AddCert(certificate)
		options.Roots = roots
		if _, err := leaf.Verify(options); err == nil {
			return nil
		}
	}
	return errors.New("agent: Center CA fingerprint mismatch")
}

func (c Client) clientFor(fingerprint, certificatePEM string, timeout time.Duration) (*http.Client, error) {
	if strings.TrimSpace(fingerprint) == "" {
		if c.HTTPClient != nil {
			return c.HTTPClient, nil
		}
		return &http.Client{Timeout: timeout}, nil
	}
	return pinnedHTTPClient(fingerprint, certificatePEM, timeout)
}

// CenterHTTPClient returns a client that enforces the persisted Center CA pin.
// It is used by command paths outside Client, including binary updates.
func CenterHTTPClient(connection Connection, timeout time.Duration) (*http.Client, error) {
	if _, _, err := normalizeCenterTrust(connection.CenterURL, connection.CAFingerprint, connection.CACertificatePEM); err != nil {
		return nil, err
	}
	if err := validateCAFingerprint(connection.CenterURL, connection.CAFingerprint); err != nil {
		return nil, err
	}
	if strings.TrimSpace(connection.CAFingerprint) == "" {
		return &http.Client{Timeout: timeout}, nil
	}
	return pinnedHTTPClient(connection.CAFingerprint, connection.CACertificatePEM, timeout)
}

func (c Client) ensureConnectionPinned(ctx context.Context, store *Store, connection Connection) (Connection, error) {
	if loopbackCenterURL(connection.CenterURL) || strings.TrimSpace(connection.CAFingerprint) != "" {
		return connection, nil
	}
	fingerprint, err := c.probeCenterCAFingerprint(ctx, connection.CenterURL)
	if err != nil {
		return Connection{}, err
	}
	connection.CAFingerprint = fingerprint
	if err := store.ReplaceConnection(ctx, connection); err != nil {
		return Connection{}, fmt.Errorf("agent: persist Center CA fingerprint: %w", err)
	}
	return connection, nil
}
