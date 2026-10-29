package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/grpc"
)

// Exercise the same certificate verifier used by gRPC with a real TLS handshake.
func pinnedTLSClientForTest(fingerprint, certificate, serverName string, timeout time.Duration) (*http.Client, error) {
	config, err := pinnedTLSConfig(fingerprint, certificate, serverName)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: config}, Timeout: timeout}, nil
}

func TestPinnedCenterRejectsDifferentCARoot(t *testing.T) {
	leafA, rootA := testPrivateCenterChain(t, "root-a")
	leafB, rootB := testPrivateCenterChain(t, "root-b")
	fingerprint := certificatePublicKeyFingerprint(rootA)
	if verifiedPresentedCA([]*x509.Certificate{leafA, rootA}, fingerprint, "center.example.test") == nil {
		t.Fatal("expected CA was rejected")
	}
	if verifiedPresentedCA([]*x509.Certificate{leafB, rootB}, fingerprint, "center.example.test") != nil {
		t.Fatal("different CA was accepted")
	}
}

func TestPinnedGRPCClientTrustsPresentedPrivateIssuingCA(t *testing.T) {
	certificate, issuer := testPrivateCenterTLSCertificate(t)
	server := httptest.NewUnstartedServer(grpc.NewServer())
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	server.StartTLS()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := newAgentGRPCConn(ctx, server.URL, certificatePublicKeyFingerprint(issuer), "", false, nil)
	if err != nil {
		t.Fatalf("private issuing CA pin failed a real TLS handshake: %v", err)
	}
	client.Close()

	_, wrongIssuer := testPrivateCenterTLSCertificate(t)
	if wrongClient, err := newAgentGRPCConn(ctx, server.URL, certificatePublicKeyFingerprint(wrongIssuer), "", false, nil); err == nil {
		wrongClient.Close()
		t.Fatalf("wrong private CA pin passed a real TLS handshake: %v", err)
	}
}

func TestPinnedHTTPClientTrustsIndependentlySuppliedPrivateCAWhenServerOmitsIt(t *testing.T) {
	certificate, issuer := testPrivateCenterTLSCertificate(t)
	certificate.Certificate = certificate.Certificate[:1]
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	server.StartTLS()
	defer server.Close()

	issuerPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuer.Raw}))
	client, err := pinnedTLSClientForTest(certificatePublicKeyFingerprint(issuer), issuerPEM, "127.0.0.1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("independently supplied private CA failed a real TLS handshake: %v", err)
	}
	response.Body.Close()
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
		DNSNames:  []string{"center.example.test"},
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
