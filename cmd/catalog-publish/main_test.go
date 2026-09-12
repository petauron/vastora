package main

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func TestSignerRequiresProtectedRegularPKCS8File(t *testing.T) {
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	path := filepath.Join(t.TempDir(), "signer.pem")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigner(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigner(path); err == nil {
		t.Fatal("world-readable signer accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := path + ".link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigner(link); err == nil {
		t.Fatal("symlink signer accepted")
	}
	if err := os.WriteFile(path, append(raw, raw...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigner(path); err == nil {
		t.Fatal("multiple PEM blocks accepted")
	}
}

func TestPublicationRequiresExplicitBootstrapOrPreviousState(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--root", "root.json", "--output", "out"},
		{"--root", "root.json", "--output", "out", "--bootstrap", "--previous", "previous.json"},
		{"--root", "root.json", "--output", "out", "--bootstrap", "--valid-for", "10000h"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("invalid publication arguments accepted: %v", args)
		}
	}
}

func TestPublisherCLIProducesIndependentlyVerifiedUpgradeAndRetainsHistory(t *testing.T) {
	directory := t.TempDir()
	root := metadata.Root(time.Now().UTC().Add(48 * time.Hour))
	root.Signed.ConsistentSnapshot = true
	var rootSigner signature.Signer
	args := []string{"--root", filepath.Join(directory, "root.json")}
	for _, role := range []string{"root", "targets", "snapshot", "timestamp"} {
		public, private, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		key, err := metadata.KeyFromPublicKey(public)
		if err != nil {
			t.Fatal(err)
		}
		if err := root.Signed.AddKey(key, role); err != nil {
			t.Fatal(err)
		}
		if role == "root" {
			rootSigner, err = signature.LoadSigner(private, crypto.Hash(0))
			if err != nil {
				t.Fatal(err)
			}
			continue // The offline root private key never enters publisher files.
		}
		der, err := x509.MarshalPKCS8PrivateKey(private)
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(directory, role+".pem")
		if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "--"+role+"-keys", file)
	}
	if _, err := root.Sign(rootSigner); err != nil {
		t.Fatal(err)
	}
	rootBytes, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "root.json"), rootBytes, 0600); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile("../../catalog/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := catalog.ParseCatalog(payload)
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(directory, "catalog.json")
	writeCatalog := func() {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(input, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeCatalog()
	first := filepath.Join(directory, "r1")
	if err := run(append(append([]string{}, args...), "--catalog", input, "--revision", "1", "--bootstrap", "--output", first)); err != nil {
		t.Fatal(err)
	}
	initial, err := catalog.VerifyOfficialRepository(context.Background(), first, "stable", rootBytes)
	if err != nil || initial.State.Acceptance.Revision != 1 {
		t.Fatalf("first signed CLI publication failed independent verification: %v", err)
	}
	appID, oldVersion := value.Apps[0].ID, value.Apps[0].Version
	value.Apps[0].Version = "99.0.0"
	writeCatalog()
	second := filepath.Join(directory, "r2")
	if err := run(append(append([]string{}, args...), "--catalog", input, "--revision", "2", "--previous", filepath.Join(first, "publication-state.json"), "--history", filepath.Join(first, "manifest-history.json"), "--output", second)); err != nil {
		t.Fatal(err)
	}
	updated, err := catalog.VerifyOfficialRepository(context.Background(), second, "stable", rootBytes)
	if err != nil || updated.State.Acceptance.Revision != 2 || updated.Catalog.Apps[0].Version != "99.0.0" {
		t.Fatalf("independent app-version publication failed verification: %v", err)
	}
	var history catalog.OfficialManifestHistory
	raw, err := os.ReadFile(filepath.Join(second, "manifest-history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &history); err != nil {
		t.Fatal(err)
	}
	if history[appID][oldVersion] == "" || history[appID]["99.0.0"] == "" {
		t.Fatal("publisher lost immutable history of an earlier app version")
	}
	value.Apps[0].Version = oldVersion
	value.Apps[0].Description.English += " changed under the old version"
	writeCatalog()
	if err := run(append(append([]string{}, args...), "--catalog", input, "--revision", "3", "--previous", filepath.Join(second, "publication-state.json"), "--history", filepath.Join(second, "manifest-history.json"), "--output", filepath.Join(directory, "rejected"))); err == nil {
		t.Fatal("publisher accepted replacement content for a historical app version")
	}
}
