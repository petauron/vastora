package catalog

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func writeOfficialRoot(t *testing.T, directory, name string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func signedOfficialRoot(t *testing.T, root *metadata.Metadata[metadata.RootType], signers ...signature.Signer) []byte {
	t.Helper()
	root.Signatures = nil
	for _, signer := range signers {
		if _, err := root.Sign(signer); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestOfficialRootDirectoryVerifiesConsecutiveRotation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	// Expired historical roots are necessary to authorize their replacements.
	oldBytes, oldKeys := officialTestRoot(t, now.Add(-72*time.Hour))
	newBytes, newKeys := officialTestRoot(t, now)
	for _, test := range []struct {
		name                string
		old, next, accepted bool
	}{
		{"both thresholds authorize rotation", true, true, true},
		{"self authorization alone is rejected", false, true, false},
		{"old authorization alone is rejected", true, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			writeOfficialRoot(t, directory, "1.root.json", oldBytes)
			writeOfficialRoot(t, directory, "README.md", []byte("Reviewed public roots only."))
			root, err := metadata.Root().FromBytes(newBytes)
			if err != nil {
				t.Fatal(err)
			}
			root.Signed.Version = 2
			var signers []signature.Signer
			if test.old {
				signers = append(signers, oldKeys[metadata.ROOT]...)
			}
			if test.next {
				signers = append(signers, newKeys[metadata.ROOT]...)
			}
			writeOfficialRoot(t, directory, "2.root.json", signedOfficialRoot(t, root, signers...))
			if err := ValidateOfficialRootDirectory(directory, now); (err == nil) != test.accepted {
				t.Fatalf("accepted=%t, error=%v", err == nil, err)
			}
		})
	}
}

func TestOfficialRootDirectoryRequiresEveryThresholdSignature(t *testing.T) {
	now := time.Now().UTC()
	raw, keys := officialTestRoot(t, now)
	root, err := metadata.Root().FromBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	otherRaw, otherKeys := officialTestRoot(t, now)
	other, err := metadata.Root().FromBytes(otherRaw)
	if err != nil {
		t.Fatal(err)
	}
	additional := other.Signed.Keys[other.Signed.Roles[metadata.ROOT].KeyIDs[0]]
	if err := root.Signed.AddKey(additional, metadata.ROOT); err != nil {
		t.Fatal(err)
	}
	root.Signed.Roles[metadata.ROOT].Threshold = 2
	directory := t.TempDir()
	writeOfficialRoot(t, directory, "1.root.json", signedOfficialRoot(t, root, keys[metadata.ROOT][0]))
	if err := ValidateOfficialRootDirectory(directory, now); err == nil {
		t.Fatal("one signature satisfied a two-key root threshold")
	}
	writeOfficialRoot(t, directory, "1.root.json", signedOfficialRoot(t, root, keys[metadata.ROOT][0], otherKeys[metadata.ROOT][0]))
	if err := ValidateOfficialRootDirectory(directory, now); err != nil {
		t.Fatalf("both authorized threshold signatures rejected: %v", err)
	}
}

func TestOfficialRootDirectoryRejectsUnsafeFilesystemInputs(t *testing.T) {
	now := time.Now().UTC()
	raw, _ := officialTestRoot(t, now)
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T, directory string)
	}{
		{"no roots", func(t *testing.T, directory string) {}},
		{"missing first root", func(t *testing.T, directory string) { writeOfficialRoot(t, directory, "2.root.json", raw) }},
		{"gap", func(t *testing.T, directory string) {
			writeOfficialRoot(t, directory, "1.root.json", raw)
			writeOfficialRoot(t, directory, "3.root.json", raw)
		}},
		{"noncanonical filename", func(t *testing.T, directory string) { writeOfficialRoot(t, directory, "01.root.json", raw) }},
		{"accidental signing key", func(t *testing.T, directory string) {
			writeOfficialRoot(t, directory, "1.root.json", raw)
			writeOfficialRoot(t, directory, "signing-key.pem", []byte("not for publication"))
		}},
		{"oversize", func(t *testing.T, directory string) {
			writeOfficialRoot(t, directory, "1.root.json", bytes.Repeat([]byte(" "), int(MaxEnvelopeBytes)+1))
		}},
		{"empty root", func(t *testing.T, directory string) { writeOfficialRoot(t, directory, "1.root.json", nil) }},
		{"root symlink", func(t *testing.T, directory string) {
			external := filepath.Join(t.TempDir(), "root.json")
			if err := os.WriteFile(external, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, filepath.Join(directory, "1.root.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"nested directory", func(t *testing.T, directory string) {
			if err := os.Mkdir(filepath.Join(directory, "1.root.json"), 0700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			test.prepare(t, directory)
			if err := ValidateOfficialRootDirectory(directory, now); err == nil {
				t.Fatal("unsafe root directory accepted")
			}
		})
	}
	directory := t.TempDir()
	writeOfficialRoot(t, directory, "1.root.json", raw)
	link := filepath.Join(t.TempDir(), "roots")
	if err := os.Symlink(directory, link); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOfficialRootDirectory(link, now); err == nil {
		t.Fatal("symlink directory accepted")
	}
	if err := ValidateOfficialRootDirectory(link+string(filepath.Separator), now); err == nil {
		t.Fatal("symlink directory with trailing separator accepted")
	}
	if err := ValidateOfficialRootDirectory(directory, time.Time{}); err == nil {
		t.Fatal("unknown local clock accepted")
	}
}

func TestOfficialRootDirectoryRejectsInvalidOrNonPublicRoots(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	raw, keys := officialTestRoot(t, now)
	for _, test := range []struct {
		name   string
		mutate func(*metadata.Metadata[metadata.RootType])
	}{
		{"expired", func(root *metadata.Metadata[metadata.RootType]) { root.Signed.Expires = now }},
		{"inconsistent snapshots", func(root *metadata.Metadata[metadata.RootType]) { root.Signed.ConsistentSnapshot = false }},
		{"wrong version", func(root *metadata.Metadata[metadata.RootType]) { root.Signed.Version = 2 }},
		{"private key material", func(root *metadata.Metadata[metadata.RootType]) {
			root.Signed.Keys[root.Signed.Roles[metadata.ROOT].KeyIDs[0]].Value.UnrecognizedFields = map[string]any{"private": "never-output-this-secret"}
		}},
		{"unknown root fields", func(root *metadata.Metadata[metadata.RootType]) {
			root.Signed.UnrecognizedFields = map[string]any{"private": "never-output-this-secret"}
		}},
		{"nil role", func(root *metadata.Metadata[metadata.RootType]) { root.Signed.Roles[metadata.TIMESTAMP] = nil }},
		{"nil key", func(root *metadata.Metadata[metadata.RootType]) {
			root.Signed.Keys[root.Signed.Roles[metadata.TARGETS].KeyIDs[0]] = nil
		}},
		{"missing role", func(root *metadata.Metadata[metadata.RootType]) { delete(root.Signed.Roles, metadata.SNAPSHOT) }},
		{"zero threshold", func(root *metadata.Metadata[metadata.RootType]) { root.Signed.Roles[metadata.TARGETS].Threshold = 0 }},
		{"impossible threshold", func(root *metadata.Metadata[metadata.RootType]) { root.Signed.Roles[metadata.TARGETS].Threshold = 2 }},
		{"duplicate role key", func(root *metadata.Metadata[metadata.RootType]) {
			role := root.Signed.Roles[metadata.TARGETS]
			role.KeyIDs = append(role.KeyIDs, role.KeyIDs[0])
		}},
		{"online root key reuse", func(root *metadata.Metadata[metadata.RootType]) {
			root.Signed.Roles[metadata.TARGETS] = root.Signed.Roles[metadata.ROOT]
		}},
		{"online root key reuse under an alternate key ID", func(root *metadata.Metadata[metadata.RootType]) {
			key := root.Signed.Keys[root.Signed.Roles[metadata.ROOT].KeyIDs[0]]
			alias := &metadata.Key{Type: key.Type, Scheme: key.Scheme, Value: metadata.KeyVal{PublicKey: strings.ToUpper(key.Value.PublicKey)}}
			id, err := alias.ID()
			if err != nil {
				t.Fatal(err)
			}
			root.Signed.Keys[id] = alias
			root.Signed.Roles[metadata.TARGETS].KeyIDs = []string{id}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, err := metadata.Root().FromBytes(raw)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(root)
			directory := t.TempDir()
			writeOfficialRoot(t, directory, "1.root.json", signedOfficialRoot(t, root, keys[metadata.ROOT][0]))
			err = ValidateOfficialRootDirectory(directory, now)
			if err == nil {
				t.Fatal("invalid public root accepted")
			}
			if strings.Contains(err.Error(), "never-output-this-secret") {
				t.Fatal("private material appeared in validation error")
			}
		})
	}
	for _, invalid := range []string{
		`{"signed":`, string(raw) + `{}`, strings.Replace(string(raw), `"consistent_snapshot":true`, `"consistent_snapshot":false,"consistent_snapshot":true`, 1),
	} {
		directory := t.TempDir()
		writeOfficialRoot(t, directory, "1.root.json", []byte(invalid))
		if err := ValidateOfficialRootDirectory(directory, now); err == nil {
			t.Fatal("invalid or ambiguous JSON accepted")
		}
	}
}
