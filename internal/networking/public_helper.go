package networking

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

func PublicAddressHTTPClient(endpoint string, allowPrivate bool) (*http.Client, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "/network/public-address" {
		return nil, "", errors.New("network: public address helper must be an exact credential-free HTTPS URL")
	}
	parsed.RawPath = ""
	normalized := parsed.String()
	expectedHost := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	transport := &http.Transport{ForceAttemptHTTP2: true, Proxy: nil, TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || strings.ToLower(strings.TrimSuffix(host, ".")) != expectedHost {
			return nil, errors.New("network: public address helper attempted an unexpected connection")
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(addresses) == 0 {
			return nil, errors.New("network: public address helper hostname could not be resolved")
		}
		for _, candidate := range addresses {
			if err := ValidateExternalHelperAddress(candidate.IP, allowPrivate); err != nil {
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
	client := &http.Client{Timeout: 10 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client, normalized, nil
}

func validatePublicHelperAddress(ip net.IP, allowPrivate bool) error {
	return ValidateExternalHelperAddress(ip, allowPrivate)
}

// ValidateExternalHelperAddress rejects addresses that external-helper URLs
// must never reach. Explicitly trusted self-hosted helpers may use private,
// loopback, shared-address, or reserved space, but metadata, link-local,
// multicast, and unspecified destinations always remain blocked.
func ValidateExternalHelperAddress(ip net.IP, allowPrivate bool) error {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return errors.New("network: external helper resolved to an invalid address")
	}
	address = address.Unmap()
	for _, raw := range []string{"169.254.169.254/32", "100.100.100.200/32", "fd00:ec2::254/128"} {
		if netip.MustParsePrefix(raw).Contains(address) {
			return errors.New("network: external helper resolved to a metadata address")
		}
	}
	if address.IsUnspecified() || address.IsMulticast() || address.IsLinkLocalUnicast() {
		return errors.New("network: external helper resolved to a disallowed address")
	}
	shared := netip.MustParsePrefix("100.64.0.0/10").Contains(address)
	if address.IsLoopback() || address.IsPrivate() || shared {
		if allowPrivate {
			return nil
		}
		return errors.New("network: external helper resolved to a private address")
	}
	for _, raw := range []string{
		"0.0.0.0/8", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"64:ff9b:1::/48", "100::/64", "2001:db8::/32",
	} {
		if netip.MustParsePrefix(raw).Contains(address) {
			if allowPrivate {
				return nil
			}
			return errors.New("network: external helper resolved to a reserved address")
		}
	}
	if !address.IsGlobalUnicast() {
		return errors.New("network: external helper resolved to a non-public address")
	}
	return nil
}
