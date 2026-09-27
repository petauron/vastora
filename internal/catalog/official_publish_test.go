package catalog

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/trustedmetadata"
)

func officialTestRoot(t *testing.T, now time.Time) ([]byte, map[string][]signature.Signer) {
	t.Helper()
	root := metadata.Root(now.Add(48 * time.Hour))
	root.Signed.ConsistentSnapshot = true
	signers := map[string][]signature.Signer{}
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
		signer, err := signature.LoadSigner(private, crypto.Hash(0))
		if err != nil {
			t.Fatal(err)
		}
		signers[role] = []signature.Signer{signer}
	}
	if _, err := root.Sign(signers["root"][0]); err != nil {
		t.Fatal(err)
	}
	rootBytes, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return rootBytes, signers
}

func TestOfficialRootRotationRequiresOldAndNewAuthorization(t *testing.T) {
	now := time.Now().UTC()
	oldBytes, oldSigners := officialTestRoot(t, now)
	newBytes, newSigners := officialTestRoot(t, now)
	for _, test := range []struct {
		name         string
		old, next    bool
		wantAccepted bool
	}{
		{name: "new key cannot authorize itself", next: true},
		{name: "old key alone cannot replace trust", old: true},
		{name: "both roots authorize rotation", old: true, next: true, wantAccepted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, err := metadata.Root().FromBytes(newBytes)
			if err != nil {
				t.Fatal(err)
			}
			root.Signed.Version = 2
			root.Signatures = nil
			if test.old {
				if _, err := root.Sign(oldSigners["root"][0]); err != nil {
					t.Fatal(err)
				}
			}
			if test.next {
				if _, err := root.Sign(newSigners["root"][0]); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			trusted, err := trustedmetadata.New(oldBytes)
			if err != nil {
				t.Fatal(err)
			}
			_, err = trusted.UpdateRoot(raw)
			if (err == nil) != test.wantAccepted {
				t.Fatalf("rotation acceptance=%t error=%v", err == nil, err)
			}
			if test.wantAccepted {
				if trusted.Root.Signed.Version != 2 {
					t.Fatal("rotated root not retained")
				}
				if _, err := trusted.UpdateRoot(oldBytes); err == nil {
					t.Fatal("revoked root replay accepted")
				}
			}
		})
	}
}

func TestOfficialPublicationVerifiesWithIndependentRoot(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rootBytes, signers := officialTestRoot(t, now)
	payload, err := os.ReadFile("testdata/v3/valid-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	expires := now.Add(36 * time.Hour)
	targetBytes, err := json.Marshal(OfficialTarget{Source: OfficialSourceIdentity, Channel: "stable", Revision: 1, GeneratedAt: now, ExpiresAt: expires, Catalog: payload})
	if err != nil {
		t.Fatal(err)
	}
	files, err := BuildOfficialRepository(rootBytes, targetBytes, nil, "stable", OfficialAcceptance{}, now, signers)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := trustedmetadata.New(rootBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := trusted.UpdateTimestamp(files["timestamp.json"]); err != nil {
		t.Fatal(err)
	}
	if _, err := trusted.UpdateSnapshot(files["1.snapshot.json"], false); err != nil {
		t.Fatal(err)
	}
	if _, err := trusted.UpdateTargets(files["1.targets.json"]); err != nil {
		t.Fatal(err)
	}
	for role, actual := range map[string]time.Time{
		"timestamp": trusted.Timestamp.Signed.Expires,
		"snapshot":  trusted.Snapshot.Signed.Expires,
		"targets":   trusted.Targets["targets"].Signed.Expires,
	} {
		if !actual.Equal(expires) {
			t.Fatalf("%s expiry %v differs from approved publication lifetime %v", role, actual, expires)
		}
	}
	if err := trusted.Targets["targets"].Signed.Targets["stable.json"].VerifyLengthHashes(targetBytes); err != nil {
		t.Fatal(err)
	}
	if err := trusted.Targets["targets"].Signed.Targets["stable.json"].VerifyLengthHashes(append(targetBytes, ' ')); err == nil {
		t.Fatal("tampered target accepted")
	}
	signers["targets"] = signers["timestamp"]
	if _, err := BuildOfficialRepository(rootBytes, targetBytes, nil, "stable", OfficialAcceptance{}, now, signers); err == nil {
		t.Fatal("unauthorized role key accepted")
	}
	sharedRoot, err := metadata.Root().FromBytes(rootBytes)
	if err != nil {
		t.Fatal(err)
	}
	sharedRoot.Signed.Roles["targets"] = sharedRoot.Signed.Roles["root"]
	sharedRoot.Signatures = nil
	if _, err := sharedRoot.Sign(signers["root"][0]); err != nil {
		t.Fatal(err)
	}
	sharedBytes, err := json.Marshal(sharedRoot)
	if err != nil {
		t.Fatal(err)
	}
	signers["targets"] = signers["root"]
	if _, err := BuildOfficialRepository(sharedBytes, targetBytes, nil, "stable", OfficialAcceptance{}, now, signers); err == nil {
		t.Fatal("online publisher was allowed to share the offline root key")
	}
}

func TestOfficialUIBundleIsVersionBoundAndSigned(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root, signers := officialTestRoot(t, now)
	payload, err := os.ReadFile("testdata/v3/valid-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	appCatalog, err := ParseCatalog(payload)
	if err != nil {
		t.Fatal(err)
	}
	appCatalog.Apps[0].ID = "meridian"
	payload, err = json.Marshal(appCatalog)
	if err != nil {
		t.Fatal(err)
	}
	target, err := json.Marshal(OfficialTarget{Source: OfficialSourceIdentity, Channel: "stable", Revision: 1, GeneratedAt: now, ExpiresAt: now.Add(time.Hour), Catalog: payload})
	if err != nil {
		t.Fatal(err)
	}
	name, err := OfficialUITargetName("meridian", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	bundle := []byte("export function mount() {}")
	style, err := OfficialUIStylesheetTargetName("meridian", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	assets := map[string][]byte{name: bundle, style: []byte(".meridian{display:block}")}
	files, err := BuildOfficialRepository(root, target, assets, "stable", OfficialAcceptance{}, now, signers)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := trustedmetadata.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := trusted.UpdateTimestamp(files["timestamp.json"]); err != nil {
		t.Fatal(err)
	}
	if _, err := trusted.UpdateSnapshot(files["1.snapshot.json"], false); err != nil {
		t.Fatal(err)
	}
	if _, err := trusted.UpdateTargets(files["1.targets.json"]); err != nil {
		t.Fatal(err)
	}
	if err := trusted.Targets["targets"].Signed.Targets[name].VerifyLengthHashes(bundle); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(bundle)
	if got := files["targets/"+hex.EncodeToString(hash[:])+"."+name]; string(got) != string(bundle) {
		t.Fatal("signed UI bundle is missing from immutable targets")
	}
	directory := t.TempDir()
	for path, raw := range files {
		location := filepath.Join(directory, path)
		if err := os.MkdirAll(filepath.Dir(location), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(location, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	verified, err := VerifyOfficialRepository(context.Background(), directory, "stable", root)
	if err != nil || string(verified.UIBundles[name]) != string(bundle) || string(verified.UIBundles[style]) != string(assets[style]) {
		t.Fatalf("signed UI bundle did not verify through the client path: %v", err)
	}
	if _, err := BuildOfficialRepository(root, target, map[string][]byte{name: bundle}, "stable", OfficialAcceptance{}, now, signers); err == nil {
		t.Fatal("incomplete UI assets were published")
	}
	for _, invalid := range []string{"ui-meridian-1.2.4.js", "ui-pulse-1.2.3.js", "ui-cpa-1.2.3.js", "../ui-meridian-1.2.3.js"} {
		if _, err := BuildOfficialRepository(root, target, map[string][]byte{invalid: bundle, style: assets[style]}, "stable", OfficialAcceptance{}, now, signers); err == nil {
			t.Fatalf("invalid UI identity %q was accepted", invalid)
		}
	}
}
