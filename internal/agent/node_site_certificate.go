package agent

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"github.com/petauron/vastora/internal/gateway"
)

// Validate the exact node-bound pair delivered by Center before activation.
func nodeSiteCertificate(hostname, chain, privateKey string) (gateway.Certificate, error) {
	if len(chain) == 0 || len(chain) > 65536 || len(privateKey) == 0 || len(privateKey) > 16384 {
		return gateway.Certificate{}, errors.New("agent: invalid node site certificate chain")
	}
	block, _ := pem.Decode([]byte(chain))
	if block == nil || block.Type != "CERTIFICATE" {
		return gateway.Certificate{}, errors.New("agent: invalid node site certificate chain")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != hostname || len(leaf.IPAddresses) != 0 || len(leaf.EmailAddresses) != 0 || len(leaf.URIs) != 0 {
		return gateway.Certificate{}, errors.New("agent: node site certificate does not match its hostname")
	}
	value := gateway.Certificate{Hostname: hostname, CertificatePEM: chain, PrivateKeyPEM: privateKey}
	if err := gateway.ValidateCertificates([]gateway.Certificate{value}); err != nil {
		return gateway.Certificate{}, errors.New("agent: node site certificate and key are invalid")
	}
	return value, nil
}
