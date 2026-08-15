package catalog

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"
)

func validCatalog() Catalog {
	return Catalog{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC),
		Apps: []AppManifest{{
			ID:          "cpa",
			Version:     "1.0.0",
			Name:        LocalizedText{English: "CPA", SimplifiedChinese: "CPA"},
			Description: LocalizedText{English: "Proxy API", SimplifiedChinese: "代理 API"},
			License:     "Apache-2.0",
			Images:      []Image{{Name: "api", Reference: "example.invalid/cpa@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
			Compose:     "services:\n  api:\n    image: example.invalid/cpa@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n",
			Config: []ConfigField{{
				Key:         "tunnel_token",
				Type:        "string",
				Label:       LocalizedText{English: "Tunnel token", SimplifiedChinese: "隧道令牌"},
				Description: LocalizedText{English: "Optional", SimplifiedChinese: "可选"},
				Secret:      true,
			}},
		}},
	}
}

func TestSignVerifyAndTamperDetection(t *testing.T) {
	t.Parallel()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := MarshalCatalog(validCatalog())
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := Sign("test-key", privateKey, payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Verify(envelope, publicKey); err != nil {
		t.Fatal(err)
	}
	firstCharacter := "A"
	if envelope.Signature[0] == 'A' {
		firstCharacter = "B"
	}
	envelope.Signature = firstCharacter + envelope.Signature[1:]
	if _, _, err := Verify(envelope, publicKey); err == nil {
		t.Fatal("expected signature failure")
	}
}

func TestSecretDefaultIsRejected(t *testing.T) {
	t.Parallel()
	catalog := validCatalog()
	value := json.RawMessage(`"not allowed"`)
	catalog.Apps[0].Config[0].Default = &value
	if err := ValidateCatalog(catalog); err == nil {
		t.Fatal("expected secret default validation failure")
	}
}
