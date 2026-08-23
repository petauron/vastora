// Package gateway contains Vastora's proxy-independent desired-state model.
package gateway

import (
	"bytes"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"
)

var hostnamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

type Upstream struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type Listener struct {
	Kind      string `json:"kind"`
	Address   string `json:"address"`
	HTTPPort  int    `json:"httpPort"`
	HTTPSPort int    `json:"httpsPort"`
}

type Route struct {
	ID           string     `json:"id"`
	Hostname     string     `json:"hostname"`
	Path         string     `json:"path,omitempty"`
	Protocol     string     `json:"protocol"`
	Upstreams    []Upstream `json:"upstreams"`
	TLSEnabled   bool       `json:"tlsEnabled"`
	ListenerKind string     `json:"listenerKind"`
	System       bool       `json:"system,omitempty"`
}

// Certificate is delivered separately from DesiredState so private keys never
// enter persisted desired-state JSON or task events.
type Certificate struct {
	Hostname       string `json:"hostname"`
	CertificatePEM string `json:"certificatePem"`
	PrivateKeyPEM  string `json:"privateKeyPem"`
}

func ValidateCertificates(values []Certificate) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validCertificateName(value.Hostname) || strings.TrimSpace(value.CertificatePEM) == "" || strings.TrimSpace(value.PrivateKeyPEM) == "" {
			return errors.New("gateway: invalid TLS certificate")
		}
		if _, exists := seen[value.Hostname]; exists {
			return fmt.Errorf("gateway: duplicate TLS certificate for %q", value.Hostname)
		}
		certificateBlock, _ := pem.Decode([]byte(value.CertificatePEM))
		keyBlock, _ := pem.Decode([]byte(value.PrivateKeyPEM))
		if certificateBlock == nil || certificateBlock.Type != "CERTIFICATE" || keyBlock == nil || keyBlock.Type != "PRIVATE KEY" {
			return fmt.Errorf("gateway: invalid TLS certificate for %q", value.Hostname)
		}
		certificate, certificateErr := x509.ParseCertificate(certificateBlock.Bytes)
		privateKey, keyErr := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		signer, signerOK := privateKey.(crypto.Signer)
		if certificateErr != nil || keyErr != nil || !signerOK || !certificateCoversName(certificate, value.Hostname) || time.Now().Before(certificate.NotBefore) || !time.Now().Before(certificate.NotAfter) {
			return fmt.Errorf("gateway: invalid TLS certificate for %q", value.Hostname)
		}
		certificatePublicKey, certificateKeyErr := x509.MarshalPKIXPublicKey(certificate.PublicKey)
		privatePublicKey, privateKeyErr := x509.MarshalPKIXPublicKey(signer.Public())
		if certificateKeyErr != nil || privateKeyErr != nil || !bytes.Equal(certificatePublicKey, privatePublicKey) {
			return fmt.Errorf("gateway: TLS certificate and private key do not match for %q", value.Hostname)
		}
		seen[value.Hostname] = struct{}{}
	}
	return nil
}

func ValidateCertificatesForState(state DesiredState, values []Certificate) error {
	if err := ValidateCertificates(values); err != nil {
		return err
	}
	for _, route := range state.Routes {
		if !route.TLSEnabled || route.ListenerKind == "public" {
			continue
		}
		covered := false
		for _, certificate := range values {
			if CertificateCoversHostname(certificate, route.Hostname) {
				covered = true
				break
			}
		}
		if !covered {
			return fmt.Errorf("gateway: TLS route %q has no matching certificate", route.Hostname)
		}
	}
	return nil
}

func CertificateCoversHostname(value Certificate, hostname string) bool {
	block, _ := pem.Decode([]byte(value.CertificatePEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return false
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	return err == nil && certificate.VerifyHostname(hostname) == nil
}

func validCertificateName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(name, "*.") {
		return strings.Count(name, "*") == 1 && hostnamePattern.MatchString(strings.TrimPrefix(name, "*."))
	}
	return hostnamePattern.MatchString(name)
}

func certificateCoversName(certificate *x509.Certificate, name string) bool {
	if strings.HasPrefix(name, "*.") {
		for _, dnsName := range certificate.DNSNames {
			if strings.EqualFold(dnsName, name) {
				return true
			}
		}
		return false
	}
	return certificate.VerifyHostname(name) == nil
}

// Layer4Route is a raw TCP upstream selected from the TLS ClientHello SNI.
// TLS remains end-to-end; HAProxy never terminates certificates.
type Layer4Route struct {
	ID        string     `json:"id"`
	Hostname  string     `json:"hostname"`
	Upstreams []Upstream `json:"upstreams"`
}

// SharedHTTPS describes the optional public TCP frontend that owns port 443.
// Unknown and Web SNI values are passed through to Caddy, which remains the
// HTTPS endpoint and certificate manager.
type SharedHTTPS struct {
	Address      string        `json:"address"`
	Port         int           `json:"port"`
	CaddyAddress string        `json:"caddyAddress"`
	CaddyPort    int           `json:"caddyPort"`
	Routes       []Layer4Route `json:"routes"`
}

type DesiredState struct {
	Revision    int64        `json:"revision"`
	Listeners   []Listener   `json:"listeners"`
	Routes      []Route      `json:"routes"`
	SharedHTTPS *SharedHTTPS `json:"sharedHttps,omitempty"`
}

func (state DesiredState) Validate() error {
	if state.Revision < 1 {
		return errors.New("gateway: revision must be positive")
	}
	seenIDs := make(map[string]struct{}, len(state.Routes))
	seenMatches := make(map[string]struct{}, len(state.Routes))
	listeners := make(map[string]Listener, len(state.Listeners))
	for _, listener := range state.Listeners {
		if listener.Kind != "lan" && listener.Kind != "headscale" && listener.Kind != "public" && listener.Kind != "system" {
			return fmt.Errorf("gateway: unsupported listener kind %q", listener.Kind)
		}
		if _, exists := listeners[listener.Kind]; exists {
			return fmt.Errorf("gateway: duplicate listener kind %q", listener.Kind)
		}
		if net.ParseIP(listener.Address) == nil || listener.HTTPPort < 1 || listener.HTTPPort > 65535 || listener.HTTPSPort < 1 || listener.HTTPSPort > 65535 {
			return fmt.Errorf("gateway: invalid %s listener", listener.Kind)
		}
		listeners[listener.Kind] = listener
	}
	for _, route := range state.Routes {
		if strings.TrimSpace(route.ID) == "" {
			return errors.New("gateway: route id is required")
		}
		if _, exists := seenIDs[route.ID]; exists {
			return fmt.Errorf("gateway: duplicate route id %q", route.ID)
		}
		seenIDs[route.ID] = struct{}{}
		if !hostnamePattern.MatchString(route.Hostname) {
			return fmt.Errorf("gateway: invalid hostname %q", route.Hostname)
		}
		if route.Path != "" && (!strings.HasPrefix(route.Path, "/") || strings.ContainsAny(route.Path, "?#") || len(route.Path) > 2048) {
			return fmt.Errorf("gateway: route %q has an invalid exact path", route.ID)
		}
		matchKey := route.ListenerKind + "\x00" + route.Hostname + "\x00" + route.Path
		if _, exists := seenMatches[matchKey]; exists {
			return fmt.Errorf("gateway: duplicate hostname and path %q", route.Hostname+route.Path)
		}
		seenMatches[matchKey] = struct{}{}
		if _, exists := listeners[route.ListenerKind]; !exists {
			return fmt.Errorf("gateway: route %q references an unavailable listener", route.ID)
		}
		if route.Protocol != "http" && route.Protocol != "https" {
			return fmt.Errorf("gateway: unsupported protocol %q", route.Protocol)
		}
		if len(route.Upstreams) == 0 {
			return fmt.Errorf("gateway: route %q requires an upstream", route.ID)
		}
		for _, upstream := range route.Upstreams {
			if net.ParseIP(upstream.Address) == nil || upstream.Port < 1 || upstream.Port > 65535 {
				return fmt.Errorf("gateway: route %q has an invalid upstream", route.ID)
			}
		}
	}
	if state.SharedHTTPS != nil {
		shared := state.SharedHTTPS
		if net.ParseIP(shared.Address) == nil || shared.Port < 1 || shared.Port > 65535 || !net.ParseIP(shared.CaddyAddress).IsLoopback() || shared.CaddyPort < 1 || shared.CaddyPort > 65535 {
			return errors.New("gateway: invalid shared HTTPS frontend")
		}
		if shared.Address == shared.CaddyAddress && shared.Port == shared.CaddyPort {
			return errors.New("gateway: shared HTTPS frontend and Caddy backend must use different sockets")
		}
		publicListener, exists := listeners["public"]
		if !exists || publicListener.Address != shared.Address || publicListener.HTTPSPort != shared.Port {
			return errors.New("gateway: shared HTTPS frontend requires the matching public listener")
		}
		webHosts := make(map[string]bool)
		for _, route := range state.Routes {
			if route.ListenerKind == "public" && route.TLSEnabled {
				webHosts[route.Hostname] = true
			}
		}
		seenLayer4 := make(map[string]bool, len(shared.Routes))
		for _, route := range shared.Routes {
			if strings.TrimSpace(route.ID) == "" || !hostnamePattern.MatchString(route.Hostname) || len(route.Upstreams) == 0 {
				return errors.New("gateway: invalid shared HTTPS route")
			}
			if seenLayer4[route.Hostname] || webHosts[route.Hostname] {
				return fmt.Errorf("gateway: duplicate shared HTTPS hostname %q", route.Hostname)
			}
			seenLayer4[route.Hostname] = true
			for _, upstream := range route.Upstreams {
				if net.ParseIP(upstream.Address) == nil || upstream.Port < 1 || upstream.Port > 65535 {
					return fmt.Errorf("gateway: shared HTTPS route %q has an invalid upstream", route.ID)
				}
			}
		}
	}
	return nil
}

func (state DesiredState) Sorted() DesiredState {
	result := DesiredState{Revision: state.Revision, Listeners: append([]Listener(nil), state.Listeners...), Routes: append([]Route(nil), state.Routes...)}
	if state.SharedHTTPS != nil {
		shared := *state.SharedHTTPS
		shared.Routes = append([]Layer4Route(nil), state.SharedHTTPS.Routes...)
		for index := range shared.Routes {
			shared.Routes[index].Upstreams = append([]Upstream(nil), shared.Routes[index].Upstreams...)
			sort.Slice(shared.Routes[index].Upstreams, func(left, right int) bool {
				first, second := shared.Routes[index].Upstreams[left], shared.Routes[index].Upstreams[right]
				if first.Address == second.Address {
					return first.Port < second.Port
				}
				return first.Address < second.Address
			})
		}
		sort.Slice(shared.Routes, func(left, right int) bool { return shared.Routes[left].ID < shared.Routes[right].ID })
		result.SharedHTTPS = &shared
	}
	sort.Slice(result.Listeners, func(left, right int) bool { return result.Listeners[left].Kind < result.Listeners[right].Kind })
	for index := range result.Routes {
		result.Routes[index].Upstreams = append([]Upstream(nil), result.Routes[index].Upstreams...)
		sort.Slice(result.Routes[index].Upstreams, func(left, right int) bool {
			first := result.Routes[index].Upstreams[left]
			second := result.Routes[index].Upstreams[right]
			if first.Address == second.Address {
				return first.Port < second.Port
			}
			return first.Address < second.Address
		})
	}
	sort.Slice(result.Routes, func(left, right int) bool {
		first, second := result.Routes[left], result.Routes[right]
		if first.ListenerKind != second.ListenerKind {
			return first.ListenerKind < second.ListenerKind
		}
		if first.Hostname != second.Hostname {
			return first.Hostname < second.Hostname
		}
		if (first.Path != "") != (second.Path != "") {
			return first.Path != ""
		}
		if first.Path != second.Path {
			return first.Path < second.Path
		}
		return first.ID < second.ID
	})
	return result
}
