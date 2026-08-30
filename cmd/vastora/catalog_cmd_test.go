package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/catalog"
)

func catalogFixturePath(parts ...string) string {
	values := append([]string{"..", "..", "internal", "catalog", "testdata", "v3"}, parts...)
	return filepath.Join(values...)
}

func TestCatalogCLIInteroperability(t *testing.T) {
	keyDirectory := filepath.Join(t.TempDir(), "keys")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runCatalogWithIO([]string{"keygen", "--out-dir", keyDirectory}, &stdout, &stderr); err != nil {
		t.Fatalf("keygen failed: %v stderr=%q", err, stderr.String())
	}
	privatePath := filepath.Join(keyDirectory, "catalog-signing-private.key")
	publicPath := filepath.Join(keyDirectory, "catalog-signing-public.key")
	privateInfo, err := os.Stat(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	publicInfo, err := os.Stat(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	if privateInfo.Mode().Perm() != 0o600 || publicInfo.Mode().Perm() != 0o644 {
		t.Fatalf("generated key modes = private %04o public %04o", privateInfo.Mode().Perm(), publicInfo.Mode().Perm())
	}
	privateKey, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), strings.TrimSpace(string(privateKey))) {
		t.Fatal("keygen printed the private key")
	}

	catalogPath := catalogFixturePath("valid-catalog.json")
	stdout.Reset()
	if err := runCatalogWithIO([]string{"validate", "--catalog", catalogPath}, &stdout, &stderr); err != nil {
		t.Fatalf("validate failed: %v", err)
	}
	if !strings.Contains(stdout.String(), "valid catalog: 1 app(s)") {
		t.Fatalf("validate output = %q", stdout.String())
	}

	envelopePath := filepath.Join(t.TempDir(), "catalog-envelope.json")
	if err := os.Chmod(privatePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runCatalogWithIO([]string{"sign", "--catalog", catalogPath, "--private-key", privatePath, "--key-id", "integration-key", "--output", envelopePath}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "permissions are too broad") {
		t.Fatalf("sign with broad private-key permissions error = %v", err)
	}
	if err := os.Chmod(privatePath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runCatalogWithIO([]string{"sign", "--catalog", catalogPath, "--private-key", privatePath, "--key-id", "integration-key", "--output", envelopePath}, &stdout, &stderr); err != nil {
		t.Fatalf("sign failed: %v", err)
	}
	envelopeInfo, err := os.Stat(envelopePath)
	if err != nil {
		t.Fatal(err)
	}
	if envelopeInfo.Mode().Perm() != 0o644 {
		t.Fatalf("signed envelope mode = %04o", envelopeInfo.Mode().Perm())
	}
	stdout.Reset()
	if err := runCatalogWithIO([]string{"verify", "--envelope", envelopePath, "--public-key", publicPath}, &stdout, &stderr); err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if !strings.Contains(stdout.String(), `verified catalog: 1 app(s), key id: "integration-key"`) {
		t.Fatalf("verify output = %q", stdout.String())
	}

	rawEnvelope, err := os.ReadFile(envelopePath)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := catalog.ParseEnvelope(rawEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	unsafeEnvelope := envelope
	unsafeEnvelope.KeyID = "catalog-key-1\ninjected-success-line"
	unsafeRaw, err := json.Marshal(unsafeEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	unsafePath := filepath.Join(t.TempDir(), "unsafe-key-id-envelope.json")
	if err := os.WriteFile(unsafePath, unsafeRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := runCatalogWithIO([]string{"verify", "--envelope", unsafePath, "--public-key", publicPath}, &stdout, &stderr); err == nil {
		t.Fatal("verify accepted an unsafe key id")
	}
	if stdout.Len() != 0 {
		t.Fatalf("failed verify wrote stdout: %q", stdout.String())
	}
	unsafeSignPath := filepath.Join(t.TempDir(), "unsafe-key-id-signed.json")
	stdout.Reset()
	if err := runCatalogWithIO([]string{"sign", "--catalog", catalogPath, "--private-key", privatePath, "--key-id", unsafeEnvelope.KeyID, "--output", unsafeSignPath}, &stdout, &stderr); err == nil {
		t.Fatal("sign accepted an unsafe key id")
	}
	if stdout.Len() != 0 {
		t.Fatalf("failed sign wrote stdout: %q", stdout.String())
	}
	if _, err := os.Stat(unsafeSignPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed sign left an output file: %v", err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)-2] ^= 1
	envelope.Payload = base64.RawURLEncoding.EncodeToString(payload)
	tampered, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	tamperedPath := filepath.Join(t.TempDir(), "tampered-envelope.json")
	if err := os.WriteFile(tamperedPath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runCatalogWithIO([]string{"verify", "--envelope", tamperedPath, "--public-key", publicPath}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "signature verification failed") {
		t.Fatalf("tampered payload verification error = %v", err)
	}
}

func TestCatalogCLIRejectsSharedBoundaryCasesWithoutStdout(t *testing.T) {
	type contractCase struct {
		Value string `json:"value"`
		Valid bool   `json:"valid"`
	}
	var cases struct {
		Versions        []contractCase `json:"versions"`
		ImageReferences []contractCase `json:"imageReferences"`
		GeneratedAt     []contractCase `json:"generatedAt"`
	}
	rawCases, err := os.ReadFile(catalogFixturePath("contract-cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawCases, &cases); err != nil {
		t.Fatal(err)
	}
	baseCatalog, err := os.ReadFile(catalogFixturePath("valid-catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	check := func(name string, values []contractCase, mutate func(map[string]any, string)) {
		t.Helper()
		for index, test := range values {
			if test.Valid {
				continue
			}
			t.Run(fmt.Sprintf("%s-%02d", name, index), func(t *testing.T) {
				var value map[string]any
				if err := json.Unmarshal(baseCatalog, &value); err != nil {
					t.Fatal(err)
				}
				mutate(value, test.Value)
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "invalid-catalog.json")
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
				var stdout, stderr bytes.Buffer
				if err := runCatalogWithIO([]string{"validate", "--catalog", path}, &stdout, &stderr); err == nil {
					t.Fatalf("validate accepted %q", test.Value)
				}
				if stdout.Len() != 0 {
					t.Fatalf("failed validate wrote stdout: %q", stdout.String())
				}
			})
		}
	}
	check("version", cases.Versions, func(value map[string]any, candidate string) {
		value["apps"].([]any)[0].(map[string]any)["version"] = candidate
	})
	check("image-reference", cases.ImageReferences, func(value map[string]any, candidate string) {
		value["apps"].([]any)[0].(map[string]any)["images"].([]any)[0].(map[string]any)["reference"] = candidate
	})
	check("generated-at", cases.GeneratedAt, func(value map[string]any, candidate string) {
		value["generatedAt"] = candidate
	})
}

func TestCatalogKeygenDoesNotLeaveAPartialKeypair(t *testing.T) {
	keyDirectory := t.TempDir()
	publicPath := filepath.Join(keyDirectory, "catalog-signing-public.key")
	if err := os.WriteFile(publicPath, []byte("existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := runCatalogWithIO([]string{"keygen", "--out-dir", keyDirectory}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "file already exists") {
		t.Fatalf("keygen collision error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(keyDirectory, "catalog-signing-private.key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("keygen left a private key after collision: %v", err)
	}
	existing, err := os.ReadFile(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(existing) != "existing\n" {
		t.Fatalf("keygen overwrote the existing public key: %q", existing)
	}
}

func TestCatalogCLIProcessExitBehavior(t *testing.T) {
	if os.Getenv("VASTORA_CATALOG_CLI_HELPER") == "1" {
		separator := -1
		for index, argument := range os.Args {
			if argument == "--" {
				separator = index
				break
			}
		}
		if separator < 0 {
			fmt.Fprintln(os.Stderr, "missing helper argument separator")
			os.Exit(2)
		}
		if err := run(os.Args[separator+1:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	validEnvelope, err := filepath.Abs(catalogFixturePath("valid-envelope.json"))
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := filepath.Abs(catalogFixturePath("catalog-signing-public.key"))
	if err != nil {
		t.Fatal(err)
	}
	rawEnvelope, err := os.ReadFile(validEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	var unsafeEnvelope map[string]any
	if err := json.Unmarshal(rawEnvelope, &unsafeEnvelope); err != nil {
		t.Fatal(err)
	}
	unsafeEnvelope["keyId"] = "catalog-key-1\ninjected-success-line"
	unsafeRaw, err := json.Marshal(unsafeEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	unsafeEnvelopePath := filepath.Join(t.TempDir(), "unsafe-key-id-envelope.json")
	if err := os.WriteFile(unsafeEnvelopePath, unsafeRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		args []string
		want int
	}{
		{name: "verified", args: []string{"catalog", "verify", "--envelope", validEnvelope, "--public-key", publicKey}, want: 0},
		{name: "invalid", args: []string{"catalog", "verify", "--envelope", catalogFixturePath("invalid", "unknown-field.json"), "--public-key", publicKey}, want: 1},
		{name: "unsafe key id", args: []string{"catalog", "verify", "--envelope", unsafeEnvelopePath, "--public-key", publicKey}, want: 1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			arguments := append([]string{"-test.run=TestCatalogCLIProcessExitBehavior", "--"}, test.args...)
			command := exec.Command(os.Args[0], arguments...)
			command.Env = append(os.Environ(), "VASTORA_CATALOG_CLI_HELPER=1")
			err := command.Run()
			got := 0
			if err != nil {
				var exitError *exec.ExitError
				if !errors.As(err, &exitError) {
					t.Fatalf("helper process failed unexpectedly: %v", err)
				}
				got = exitError.ExitCode()
			}
			if got != test.want {
				t.Fatalf("exit code = %d, want %d", got, test.want)
			}
		})
	}
}
