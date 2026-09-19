package agent

import (
	"context"

	"errors"

	"net"

	"net/url"

	"strings"
)

func loopbackCenterURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "http" {
		return false
	}
	host := net.ParseIP(parsed.Hostname())
	return host != nil && host.IsLoopback()
}

// VerifyCenterURL validates a user- or Center-supplied control-plane address
// and confirms that it serves a healthy Center before local state is changed.
type VerifiedCenter struct {
	URL              string
	CAFingerprint    string
	CACertificatePEM string
}

// NormalizeDeferredLoopbackCenterURL validates the only Center address that
// may be saved before it is reachable. The co-located upgrade script uses this
// while the old Center is stopped and verifies the replacement before starting
// the Agent. Remote addresses must always use VerifyCenterURL instead.
func NormalizeDeferredLoopbackCenterURL(desired string) (string, error) {
	normalized, err := normalizeCenterURL(desired)
	if err != nil {
		return "", err
	}
	if !loopbackCenterURL(normalized) {
		return "", errors.New("agent: deferred Center health verification requires loopback HTTP")
	}
	return normalized, nil
}

func (c Client) VerifyCenterURL(ctx context.Context, desired, expectedCAFingerprint, caCertificatePEM string) (VerifiedCenter, error) {
	normalized, err := normalizeCenterURL(desired)
	if err != nil {
		return VerifiedCenter{}, err
	}
	fingerprint, caCertificatePEM, err := normalizeCenterTrust(normalized, expectedCAFingerprint, caCertificatePEM)
	if err != nil {
		return VerifiedCenter{}, err
	}
	if fingerprint == "" && !loopbackCenterURL(normalized) {
		fingerprint, err = c.probeCenterCAFingerprint(ctx, normalized)
		if err != nil {
			return VerifiedCenter{}, err
		}
	}
	if err := validateCAFingerprint(normalized, fingerprint); err != nil {
		return VerifiedCenter{}, err
	}
	var health struct {
		Status string `json:"status"`
	}
	if err := c.get(ctx, normalized+"/healthz", "", fingerprint, caCertificatePEM, &health); err != nil {
		return VerifiedCenter{}, err
	}
	if health.Status != "ok" {
		return VerifiedCenter{}, errors.New("health check is not OK")
	}
	return VerifiedCenter{URL: normalized, CAFingerprint: fingerprint, CACertificatePEM: caCertificatePEM}, err
}

func (c Client) verifyCenterURL(ctx context.Context, desired string) (string, string, error) {
	normalized, err := normalizeCenterURL(desired)
	if err != nil {
		return "", "", err
	}
	fingerprint, err := c.probeCenterCAFingerprint(ctx, normalized)
	if err != nil {
		return "", "", err
	}
	var health struct {
		Status string `json:"status"`
	}
	if err := c.get(ctx, normalized+"/healthz", "", fingerprint, "", &health); err != nil {
		return "", "", err
	}
	if health.Status != "ok" {
		return "", "", errors.New("health check is not OK")
	}
	return normalized, fingerprint, nil
}

func normalizeCenterURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("agent: Center URL must be an absolute URL without credentials, query, or fragment")
	}
	if parsed.Scheme == "https" {
		return strings.TrimRight(parsed.String(), "/"), nil
	}
	if parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname()) {
		return "", errors.New("agent: Center URL must use HTTPS unless it is loopback HTTP")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.To4() != nil && ip.IsLoopback()
}
