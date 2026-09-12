package catalog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOfficialStagedRepositoryUsesIndependentVerification(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root, signers := officialTestRoot(t, now)
	payload, err := os.ReadFile("testdata/v3/valid-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(OfficialTarget{Source: OfficialSourceIdentity, Channel: "stable", Revision: 1, GeneratedAt: now, ExpiresAt: now.Add(time.Hour), Catalog: payload})
	if err != nil {
		t.Fatal(err)
	}
	files, err := BuildOfficialRepository(root, raw, "stable", OfficialAcceptance{}, now, signers)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, content := range files {
		location := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(location), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(location, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := VerifyOfficialRepository(context.Background(), dir, "stable", root)
	if err != nil || result.State.Acceptance.Revision != 1 {
		t.Fatalf("staged verification: %v", err)
	}
	otherRoot, _ := officialTestRoot(t, now)
	if _, err := VerifyOfficialRepository(context.Background(), dir, "stable", otherRoot); err == nil {
		t.Fatal("same-directory root substitution accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "timestamp.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyOfficialRepository(context.Background(), dir, "stable", root); err == nil {
		t.Fatal("damaged timestamp accepted")
	}
}
