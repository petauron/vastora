//go:build ignore

package main

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"strings"
	"time"

	m "github.com/petauron/meridian"
)

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func writeFile(path string, data []byte) {
	if err := os.WriteFile(path, data, 0600); err != nil {
		panic(err)
	}
}

func main() {
	if len(os.Args) != 2 {
		panic("usage: generate.go LAB_DIRECTORY")
	}
	d := os.Args[1]
	key := must(ecdh.X25519().GenerateKey(rand.Reader))
	priv := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))
	pub := &priv.PublicKey
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "entry.example.test"}, DNSNames: []string{"entry.example.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der := must(x509.CreateCertificate(rand.Reader, template, template, pub, priv))
	pkey := must(x509.MarshalPKCS8PrivateKey(priv))
	cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	certKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkey}))
	writeFile(d+"/cert.pem", []byte(cert))
	writeFile(d+"/key.pem", []byte(certKey))
	id := "00000000-0000-4000-8000-000000000001"
	authBytes := make([]byte, 32)
	must(rand.Read(authBytes))
	auth := base64.RawURLEncoding.EncodeToString(authBytes)
	material := m.CredentialMaterial{Credential: m.Credential{ID: "native", AccountID: "account", EntryID: "entry", User: "lab", Kind: m.NativeCredential, Identity: m.Identity(id), Enabled: true}, ProtocolID: id, HysteriaAuth: auth, HysteriaIdentity: m.Identity(auth)}
	artifact, err := m.BuildDesiredArtifact(m.XrayPlan{Revision: 1, RealityEndpoints: []m.RealityEndpoint{{ID: "entry", EntryID: "entry", InboundTag: "entry", ListenAddress: "192.168.240.2", ListenPort: m.RealityBackendPort, AdvertiseHost: "entry.example.test", AdvertisePort: 443, Target: "192.168.240.4:443", ServerNames: []string{"entry.example.test"}, PrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes()), PublicKey: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), ShortIDs: []string{"0123456789abcdef"}, Fingerprint: "chrome"}}, HysteriaEndpoints: []m.HysteriaEndpoint{{ID: "hy2", EntryID: "entry", InboundTag: "hy2", ListenPort: 443, AdvertiseHost: "entry.example.test", AdvertisePort: 443, ServerName: "entry.example.test", CertificatePEM: cert, PrivateKeyPEM: certKey}}, Credentials: []m.CredentialMaterial{material}})
	if err != nil {
		panic(err)
	}
	writeFile(d+"/rendered.json", artifact.Config)
	// Only the isolated fixture may reach private STUN destinations. Production
	// keeps Xray's default private-destination policy unchanged.
	var server map[string]any
	if err := json.Unmarshal(artifact.Config, &server); err != nil {
		panic(err)
	}
	direct := server["outbounds"].([]any)[0].(map[string]any)
	if direct["protocol"] != "freedom" {
		panic("unexpected direct outbound")
	}
	direct["settings"] = map[string]any{"finalRules": []any{map[string]any{"action": "allow", "network": "udp", "ip": []string{"192.168.240.4/32", "192.168.240.5/32"}, "port": "3478-3479"}}}
	fixture, err := json.MarshalIndent(server, "", "  ")
	if err != nil {
		panic(err)
	}
	writeFile(d+"/server.json", fixture)
	write := func(name string, v any) { writeFile(d+"/"+name, must(json.MarshalIndent(v, "", "  "))) }
	vless := map[string]any{"protocol": "vless", "settings": map[string]any{"address": "192.168.240.2", "port": 443, "id": id, "encryption": "none", "flow": "xtls-rprx-vision-udp443"}, "streamSettings": map[string]any{"method": "raw", "security": "reality", "realitySettings": map[string]any{"serverName": "entry.example.test", "fingerprint": "chrome", "password": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "shortId": "0123456789abcdef"}}, "mux": map[string]any{"enabled": true, "concurrency": -1, "xudpConcurrency": 16, "xudpProxyUDP443": "allow"}}
	hy2 := map[string]any{"protocol": "hysteria", "settings": map[string]any{"version": 2, "address": "192.168.240.2", "port": 443}, "streamSettings": map[string]any{"method": "hysteria", "security": "tls", "hysteriaSettings": map[string]any{"version": 2, "auth": auth}, "tlsSettings": map[string]any{"serverName": "entry.example.test", "alpn": []string{"h3"}, "certificates": []any{map[string]any{"usage": "verify", "certificate": strings.Split(strings.TrimSpace(cert), "\n")}}}}}
	for name, out := range map[string]any{"vless": vless, "hy2": hy2} {
		write(name+".json", map[string]any{"log": map[string]any{"loglevel": "warning"}, "inbounds": []any{map[string]any{"listen": "0.0.0.0", "port": 1080, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": true, "ip": "192.168.240.3"}}}, "outbounds": []any{out}})
	}
}
