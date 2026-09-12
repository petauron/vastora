package catalog

import (
	"crypto"
	"crypto/ed25519"
	"encoding/json"
	"os"
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
	files, err := BuildOfficialRepository(rootBytes, targetBytes, "stable", OfficialAcceptance{}, now, signers)
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
	if _, err := BuildOfficialRepository(rootBytes, targetBytes, "stable", OfficialAcceptance{}, now, signers); err == nil {
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
	if _, err := BuildOfficialRepository(sharedBytes, targetBytes, "stable", OfficialAcceptance{}, now, signers); err == nil {
		t.Fatal("online publisher was allowed to share the offline root key")
	}
}
