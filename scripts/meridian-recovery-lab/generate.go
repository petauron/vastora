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
	"github.com/petauron/vastora/internal/meridianruntime"
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
	material := m.CredentialMaterial{Credential: m.Credential{ID: "native", AccountID: "account", EntryID: "entry", User: "lab", Kind: m.NativeCredential, Identity: m.Identity(id), Enabled: true}, ProtocolID: id}
	artifact, err := m.BuildDesiredArtifact(m.XrayPlan{Revision: 1, RealityEndpoints: []m.RealityEndpoint{{ID: "entry", EntryID: "entry", InboundTag: "entry", ListenAddress: "192.168.241.2", ListenPort: m.RealityBackendPort, AdvertiseHost: "entry.example.test", AdvertisePort: 443, Target: "192.168.241.4:443", ServerNames: []string{"entry.example.test"}, PrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes()), PublicKey: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), ShortIDs: []string{"0123456789abcdef"}, Fingerprint: "chrome"}}, Credentials: []m.CredentialMaterial{material, {Credential: m.Credential{ID: "fixed", AccountID: "account", EntryID: "entry", EgressID: "landing", User: "lab-fixed", Kind: m.RouteCredential, Identity: m.Identity("00000000-0000-4000-8000-000000000002"), Enabled: true}, ProtocolID: "00000000-0000-4000-8000-000000000002"}}, Peers: []m.RoutePeer{{EgressID: "landing", Address: "192.168.241.5", Port: 1080}}, Grants: []m.RouteGrant{{ID: "fixed-route", AccountID: "account", EntryID: "entry", EgressID: "landing", InboundTag: "entry", Base: material.Credential, Route: m.Credential{ID: "fixed", AccountID: "account", EntryID: "entry", EgressID: "landing", User: "lab-fixed", Kind: m.RouteCredential, Identity: m.Identity("00000000-0000-4000-8000-000000000002"), Enabled: true}, Mode: m.FixedMode, Enabled: true, DesiredRev: 1}}})
	if err != nil {
		panic(err)
	}
	writeFile(d+"/rendered.json", artifact.Config)
	// Only this isolated fixture may reach its synthetic HTTP witness.
	// Production private-destination restrictions remain unchanged.
	var server map[string]any
	if err := json.Unmarshal(artifact.Config, &server); err != nil {
		panic(err)
	}
	direct := server["outbounds"].([]any)[0].(map[string]any)
	if direct["protocol"] != "freedom" {
		panic("unexpected direct outbound")
	}
	direct["settings"] = map[string]any{"finalRules": []any{map[string]any{"action": "allow", "network": "tcp", "ip": []string{"192.168.241.4/32"}, "port": "8080"}}}
	fixture, err := json.MarshalIndent(server, "", "  ")
	if err != nil {
		panic(err)
	}
	writeFile(d+"/server-initial.json", fixture)
	// Preserve the exact runtime and business credentials; change only the
	// replacement host's approved listen address in the isolated fixture.
	writeFile(d+"/server-replacement.json", []byte(strings.ReplaceAll(string(fixture), "192.168.241.2", "192.168.241.7")))
	write := func(name string, v any) { writeFile(d+"/"+name, must(json.MarshalIndent(v, "", "  "))) }
	public := m.RealityEndpoint{ID: "entry", EntryID: "entry", AdvertiseHost: "192.168.241.6", AdvertisePort: 443, ServerNames: []string{"entry.example.test"}, PublicKey: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), ShortIDs: []string{"0123456789abcdef"}, Fingerprint: "chrome"}
	for name, credential := range map[string]string{"native": id, "fixed": "00000000-0000-4000-8000-000000000002", "invalid": "00000000-0000-4000-8000-000000000003"} {
		selected := material
		selected.Credential.ID = name
		selected.Credential.Identity = m.Identity(credential)
		selected.ProtocolID = credential
		if name == "fixed" {
			selected.Credential.Kind = m.RouteCredential
			selected.Credential.EgressID = "landing"
		}
		config := must((meridianruntime.AcceptanceClient{Protocol: m.VLESSReality, Reality: &public, Material: selected}).Config(1080))
		writeFile(d+"/"+name+".json", config)
	}
	write("landing.json", map[string]any{"log": map[string]any{"loglevel": "none"}, "inbounds": []any{map[string]any{"listen": "0.0.0.0", "port": 1080, "protocol": "socks", "settings": map[string]any{"auth": "noauth"}}}, "outbounds": []any{map[string]any{"protocol": "freedom", "settings": map[string]any{"finalRules": []any{map[string]any{"action": "allow", "network": "tcp", "ip": []string{"192.168.241.4/32"}, "port": "8080"}}}}}})
}
