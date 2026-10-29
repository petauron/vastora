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
	"net/url"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
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
	parsed, err := url.Parse(centerURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return "", errors.New("agent: invalid Center TLS identity")
	}
	conn, err := newAgentGRPCConn(centerURL, "", "", true, nil)
	if err != nil {
		return "", fmt.Errorf("agent: prepare Center TLS identity probe: %w", err)
	}
	defer conn.Close()
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var remote peer.Peer
	response, err := agentpb.NewAgentControlClient(conn).Probe(probeCtx, &agentpb.ProbeRequest{}, grpc.Peer(&remote))
	if err != nil {
		return "", fmt.Errorf("agent: verify Center TLS identity: %w", err)
	}
	info, ok := remote.AuthInfo.(credentials.TLSInfo)
	if !ok || response == nil || !response.Ready {
		return "", errors.New("agent: Center did not provide a verifiable gRPC identity")
	}
	state := info.State
	if len(state.VerifiedChains) == 0 || state.NegotiatedProtocol != "h2" {
		return "", errors.New("agent: Center does not provide verified gRPC transport")
	}
	for index := len(state.PeerCertificates) - 1; index > 0; index-- {
		if state.PeerCertificates[index].IsCA {
			return certificatePublicKeyFingerprint(state.PeerCertificates[index]), nil
		}
	}
	root := state.VerifiedChains[0][len(state.VerifiedChains[0])-1]
	return certificatePublicKeyFingerprint(root), nil
}

func certificatePublicKeyFingerprint(certificate *x509.Certificate) string {
	digest := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(digest[:])
}

func pinnedTLSConfig(fingerprint, certificatePEM string) (*tls.Config, error) {
	fingerprint = normalizeCAFingerprint(fingerprint)
	if _, err := hex.DecodeString(fingerprint); err != nil || len(fingerprint) != sha256.Size*2 {
		return nil, errors.New("agent: invalid Center CA fingerprint")
	}
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
		return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, VerifyConnection: func(state tls.ConnectionState) error {
			for _, chain := range state.VerifiedChains {
				if len(chain) != 0 && certificatePublicKeyFingerprint(chain[len(chain)-1]) == fingerprint {
					return nil
				}
			}
			return errors.New("agent: Center CA fingerprint mismatch")
		}}, nil
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true, // The chain and hostname are verified below against the pinned CA.
		VerifyConnection: func(state tls.ConnectionState) error { return verifyPinnedCA(state, fingerprint) }}, nil
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
