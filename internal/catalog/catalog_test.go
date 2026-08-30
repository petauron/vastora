package catalog

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
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
	tamperedPayload, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
	if err != nil {
		t.Fatal(err)
	}
	tamperedPayload[len(tamperedPayload)-2] ^= 1
	envelope.Payload = base64.RawURLEncoding.EncodeToString(tamperedPayload)
	if _, _, err := Verify(envelope, publicKey); err == nil {
		t.Fatal("expected one changed payload byte to invalidate the signature")
	}
}

func TestPortableCatalogFixtures(t *testing.T) {
	t.Parallel()
	valid, err := os.ReadFile(filepath.Join("testdata", "v3", "valid-catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCatalog(valid); err != nil {
		t.Fatalf("portable valid fixture was rejected: %v", err)
	}
	additionalValid, err := filepath.Glob(filepath.Join("testdata", "v3", "valid", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(additionalValid) == 0 {
		t.Fatal("additional portable valid fixtures are missing")
	}
	for _, path := range additionalValid {
		path := path
		t.Run("valid-"+filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseCatalog(raw); err != nil {
				t.Fatalf("valid portable fixture was rejected: %v", err)
			}
		})
	}
	invalid, err := filepath.Glob(filepath.Join("testdata", "v3", "invalid", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(invalid) == 0 {
		t.Fatal("portable invalid fixtures are missing")
	}
	for _, path := range invalid {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseCatalog(raw); err == nil {
				t.Fatal("invalid portable fixture was accepted")
			}
		})
	}
}

func TestOfficialCatalogMatchesCurrentContract(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "catalog", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCatalog(raw); err != nil {
		t.Fatalf("official catalog was rejected: %v", err)
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

func TestConfigDefaultMustMatchDeclaredType(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		fieldType string
		value     string
	}{
		{fieldType: "string", value: `true`},
		{fieldType: "boolean", value: `"true"`},
		{fieldType: "integer", value: `1.5`},
		{fieldType: "integer", value: `"1"`},
		{fieldType: "string", value: `null`},
	} {
		test := test
		t.Run(test.fieldType+"-"+test.value, func(t *testing.T) {
			t.Parallel()
			c := validCatalog()
			c.Apps[0].Config[0].Secret = false
			c.Apps[0].Config[0].Type = test.fieldType
			value := json.RawMessage(test.value)
			c.Apps[0].Config[0].Default = &value
			if err := ValidateCatalog(c); err == nil {
				t.Fatal("mismatched config default was accepted")
			}
		})
	}
}

func TestPortableIntegerDefaults(t *testing.T) {
	t.Parallel()
	for _, value := range []string{`1`, `1.0`, `1e0`, `-9007199254740991`, `9007199254740991`} {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			c := validCatalog()
			c.Apps[0].Config[0].Secret = false
			c.Apps[0].Config[0].Type = "integer"
			defaultValue := json.RawMessage(value)
			c.Apps[0].Config[0].Default = &defaultValue
			if err := ValidateCatalog(c); err != nil {
				t.Fatalf("portable integer default was rejected: %v", err)
			}
		})
	}
	for _, value := range []string{`1.5`, `9007199254740992`, `-9007199254740992`} {
		value := value
		t.Run("reject-"+value, func(t *testing.T) {
			t.Parallel()
			c := validCatalog()
			c.Apps[0].Config[0].Secret = false
			c.Apps[0].Config[0].Type = "integer"
			defaultValue := json.RawMessage(value)
			c.Apps[0].Config[0].Default = &defaultValue
			if err := ValidateCatalog(c); err == nil {
				t.Fatal("non-portable integer default was accepted")
			}
		})
	}
}

func TestAppAndImageIDsMayStartWithDigits(t *testing.T) {
	t.Parallel()
	catalog := validCatalog()
	catalog.Apps[0].ID = "3x-ui"
	catalog.Apps[0].Images[0].Name = "3x-ui"
	if err := ValidateCatalog(catalog); err != nil {
		t.Fatalf("expected numeric-leading identifiers to be valid: %v", err)
	}
}

func TestNativeArtifactsArePlatformPinned(t *testing.T) {
	t.Parallel()
	c := validCatalog()
	c.Apps[0].Images = nil
	c.Apps[0].Artifacts = []Artifact{{
		Name: "komari-agent", OperatingSystem: "linux", Architecture: "arm64",
		URL:    "https://github.com/komari-monitor/komari-agent/releases/download/1.2.60/komari-agent-linux-arm64",
		SHA256: "8d98966365848435f756d00435b42654d56557f5f783c4b16ba83ed413038007",
	}}
	if err := ValidateCatalog(c); err != nil {
		t.Fatalf("expected a pinned native artifact to be valid: %v", err)
	}
	validURL := c.Apps[0].Artifacts[0].URL
	for _, invalidURL := range []string{validURL + "?latest=1", validURL + "#download", validURL + "?", validURL + "#", " " + validURL, validURL + " "} {
		c.Apps[0].Artifacts[0].URL = invalidURL
		if err := ValidateCatalog(c); err == nil {
			t.Fatalf("expected artifact URL %q to be rejected", invalidURL)
		}
	}
	c.Apps[0].Artifacts[0].URL = validURL
	c.Apps[0].Artifacts[0].OperatingSystem = " linux"
	if err := ValidateCatalog(c); err == nil {
		t.Fatal("expected a non-canonical artifact platform to be rejected")
	}
}

func TestHomepageMustReferenceADeclaredService(t *testing.T) {
	t.Parallel()
	catalog := validCatalog()
	catalog.Apps[0].Homepage = &Homepage{Service: "manager", Path: "/"}
	if err := ValidateCatalog(catalog); err == nil {
		t.Fatal("expected unknown homepage service validation failure")
	}
	catalog.Apps[0].Services = []Service{{Name: "manager", Protocol: "http", ContainerPort: 8080, DefaultHostPort: 8080}}
	if err := ValidateCatalog(catalog); err != nil {
		t.Fatalf("expected declared homepage service to be valid: %v", err)
	}
	catalog.Apps[0].Homepage.Path = "https://example.invalid/"
	if err := ValidateCatalog(catalog); err == nil {
		t.Fatal("expected absolute homepage URL validation failure")
	}
}

func TestHealthPathMustBeAPlainAbsolutePath(t *testing.T) {
	t.Parallel()
	catalog := validCatalog()
	catalog.Apps[0].Services = []Service{{Name: "manager", Protocol: "http", ContainerPort: 8080, DefaultHostPort: 8080, HealthPath: "/healthz"}}
	if err := ValidateCatalog(catalog); err != nil {
		t.Fatalf("expected plain health path to be valid: %v", err)
	}
	for _, path := range []string{"healthz", "//example.invalid/", "/healthz?token=secret", "/healthz#fragment"} {
		catalog.Apps[0].Services[0].HealthPath = path
		if err := ValidateCatalog(catalog); err == nil {
			t.Fatalf("expected health path %q to be rejected", path)
		}
	}
}

func TestServiceHasOneHostPortSource(t *testing.T) {
	t.Parallel()
	c := validCatalog()
	c.Apps[0].Config = append(c.Apps[0].Config, ConfigField{
		Key: "listen_port", Type: "integer",
		Label:       LocalizedText{English: "Listen port", SimplifiedChinese: "监听端口"},
		Description: LocalizedText{English: "Port", SimplifiedChinese: "端口"},
	})
	c.Apps[0].Services = []Service{{Name: "manager", Protocol: "http", ContainerPort: 8080, DefaultHostPort: 8080, HostPortField: "listen_port"}}
	if err := ValidateCatalog(c); err == nil {
		t.Fatal("service with two host port sources was accepted")
	}
}
