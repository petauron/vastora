package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPinnedCenterRejectsDifferentCARoot(t *testing.T) {
	leafA, rootA := testPrivateCenterChain(t, "root-a")
	leafB, rootB := testPrivateCenterChain(t, "root-b")
	fingerprint := certificatePublicKeyFingerprint(rootA)
	if err := verifyPinnedCA(tls.ConnectionState{PeerCertificates: []*x509.Certificate{leafA, rootA}, ServerName: "center.example.test"}, fingerprint); err != nil {
		t.Fatalf("expected CA was rejected: %v", err)
	}
	if err := verifyPinnedCA(tls.ConnectionState{PeerCertificates: []*x509.Certificate{leafB, rootB}, ServerName: "center.example.test"}, fingerprint); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("different CA was accepted: %v", err)
	}
}

func TestPinnedHTTPClientTrustsPresentedPrivateIssuingCA(t *testing.T) {
	certificate, issuer := testPrivateCenterTLSCertificate(t)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	server.StartTLS()
	defer server.Close()

	client, err := pinnedHTTPClient(certificatePublicKeyFingerprint(issuer), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("private issuing CA pin failed a real TLS handshake: %v", err)
	}
	response.Body.Close()

	_, wrongIssuer := testPrivateCenterTLSCertificate(t)
	wrongClient, err := pinnedHTTPClient(certificatePublicKeyFingerprint(wrongIssuer), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongClient.Get(server.URL); err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("wrong private CA pin passed a real TLS handshake: %v", err)
	}
}

func testPrivateCenterTLSCertificate(t *testing.T) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	rootPublic, rootPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rootTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(11), Subject: pkix.Name{CommonName: "private-root"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, rootPublic, rootPrivate)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	issuerPublic, issuerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuerTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(12), Subject: pkix.Name{CommonName: "private-issuer"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	issuerDER, err := x509.CreateCertificate(rand.Reader, issuerTemplate, root, issuerPublic, rootPrivate)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := x509.ParseCertificate(issuerDER)
	if err != nil {
		t.Fatal(err)
	}
	leafPublic, leafPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(13), Subject: pkix.Name{CommonName: "127.0.0.1"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, issuer, leafPublic, issuerPrivate)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{leafDER, issuerDER}, PrivateKey: leafPrivate}, issuer
}

func testPrivateCenterChain(t *testing.T, commonName string) (*x509.Certificate, *x509.Certificate) {
	t.Helper()
	rootPublic, rootPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rootTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: commonName},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, rootPublic, rootPrivate)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	leafPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "center.example.test"}, DNSNames: []string{"center.example.test"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, root, leafPublic, rootPrivate)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}
	return leaf, root
}
