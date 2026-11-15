package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

func assetTLS() error {
	if _, err := os.Stat(filepath.Join(root, "asset-cert.pem")); err == nil {
		return assetLeaf()
	} else if !os.IsNotExist(err) {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return err
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "isolated-artifacts"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return err
	}
	raw, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "asset-cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "asset-key.pem"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: raw}), 0600); err != nil {
		return err
	}
	return assetLeaf()
}
func assetClient() (*http.Client, error) {
	raw, err := os.ReadFile(filepath.Join(root, "asset-cert.pem"))
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		return nil, errors.New("invalid fixture artifact CA")
	}
	return &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}, nil
}
func assetServer() (*http.Server, error) {
	if err := assetTLS(); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "assets")
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, err
	}
	return &http.Server{Addr: "127.0.0.1:18081", Handler: http.FileServer(http.Dir(path)), ReadHeaderTimeout: 5 * time.Second}, nil
}

func senlunProxy() *http.Server {
	target, _ := url.Parse("http://127.0.0.1:18090")
	return &http.Server{Addr: "127.0.0.1:18091", Handler: httputil.NewSingleHostReverseProxy(target), ReadHeaderTimeout: 5 * time.Second}
}

func headscaleProxy() *http.Server {
	target, _ := url.Parse("http://127.0.0.1:18080")
	return &http.Server{Addr: "127.0.0.1:18443", Handler: httputil.NewSingleHostReverseProxy(target), ReadHeaderTimeout: 5 * time.Second}
}

func assetLeaf() error {
	if _, err := os.Stat(filepath.Join(root, "asset-leaf.pem")); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	certRaw, err := os.ReadFile(filepath.Join(root, "asset-cert.pem"))
	if err != nil {
		return err
	}
	keyRaw, err := os.ReadFile(filepath.Join(root, "asset-key.pem"))
	if err != nil {
		return err
	}
	caBlock, _ := pem.Decode(certRaw)
	keyBlock, _ := pem.Decode(keyRaw)
	if caBlock == nil || keyBlock == nil {
		return errors.New("fixture signing identity unavailable")
	}
	ca, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return err
	}
	signer, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return err
	}
	leaf := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "isolated-https"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(48 * time.Hour), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("100.126.0.2")}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, signer)
	if err != nil {
		return err
	}
	raw, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "asset-leaf.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "asset-leaf-key.pem"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: raw}), 0600)
}
