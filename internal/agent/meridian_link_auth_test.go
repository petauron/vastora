package agent

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/nodediagnostics"
)

func TestMeridianLinkAuthRequiresCanonical256BitTokens(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	tokenBytes := bytes.Repeat([]byte{0x42}, nodediagnostics.LinkBandwidthCredentialBytes)
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	client := nodediagnostics.LinkBandwidthAuth{KeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public})), Password: token}
	server := nodediagnostics.LinkBandwidthAuth{KeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), PasswordHash: hex.EncodeToString(bytes.Repeat([]byte{0x42}, 32))}
	if err := validateMeridianLinkAuth(&client, false); err != nil {
		t.Fatalf("canonical token rejected: %v", err)
	}
	if err := validateMeridianLinkAuth(&server, true); err != nil {
		t.Fatalf("protocol digest rejected: %v", err)
	}
	for name, value := range map[string]string{
		"human password": strings.Repeat("password", 8),
		"short entropy":  base64.RawURLEncoding.EncodeToString(tokenBytes[:31]),
		"long entropy":   base64.RawURLEncoding.EncodeToString(append(bytes.Clone(tokenBytes), 0x42)),
		"padded":         base64.URLEncoding.EncodeToString(tokenBytes),
		"newline":        token + "\n",
		"invalid base64": strings.Repeat("!", len(token)),
		"noncanonical":   token[:len(token)-1] + "J",
	} {
		t.Run(name, func(t *testing.T) {
			invalid := client
			invalid.Password = value
			if err := validateMeridianLinkAuth(&invalid, false); err == nil {
				t.Fatal("malformed or noncanonical probe token accepted")
			}
		})
	}
	client.PasswordHash = server.PasswordHash
	if err := validateMeridianLinkAuth(&client, false); err == nil {
		t.Fatal("server digest accepted in the client role")
	}
	server.Password = token
	if err := validateMeridianLinkAuth(&server, true); err == nil {
		t.Fatal("plaintext token accepted in the server role")
	}
	server.Password = ""
	server.PasswordHash = strings.Repeat("z", 64)
	if err := validateMeridianLinkAuth(&server, true); err == nil {
		t.Fatal("malformed protocol digest accepted")
	}
}
