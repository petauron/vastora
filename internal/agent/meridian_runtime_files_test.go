package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/glivela"
)

func compositeFilesFixture(t *testing.T) meridian.DesiredArtifact {
	t.Helper()
	files := map[string][]byte{}
	paths := map[string]string{}
	for _, kind := range []string{"routing"} {
		contents := []byte("-- anonymous " + kind + " fixture\n")
		digest := sha256.Sum256(contents)
		name := "glivela-" + kind + "-" + hex.EncodeToString(digest[:]) + ".lua"
		files[name], paths[kind] = contents, "/etc/vastora/"+name
	}
	config, err := json.Marshal(map[string]any{"routing": map[string]string{"script": paths["routing"]}})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(config)
	return meridian.DesiredArtifact{Revision: 1, Config: config, ConfigSHA256: hex.EncodeToString(digest[:]), Files: files}
}

func TestCompositeRuntimeFilesAreImmutableAndRetrySafe(t *testing.T) {
	store := &Store{dataDir: t.TempDir()}
	dir := filepath.Join(store.dataDir, meridianRuntimeDirectory)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	artifact := compositeFilesFixture(t)
	for i := 0; i < 2; i++ {
		if err := store.stageMeridianFiles(artifact, os.Geteuid()); err != nil {
			t.Fatal(err)
		}
	}
	for name := range artifact.Files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("changed"), 0600); err != nil {
			t.Fatal(err)
		}
		if store.verifyMeridianFiles(artifact) == nil || store.stageMeridianFiles(artifact, os.Geteuid()) == nil {
			t.Fatal("modified script was adopted or overwritten")
		}
		break
	}
}

func TestCompositeRuntimeFileManifestRejectsTraversalAndExtraFiles(t *testing.T) {
	artifact := compositeFilesFixture(t)
	artifact.Files["../outside.lua"] = []byte(strings.Repeat("x", 10))
	if artifact.Validate() == nil {
		t.Fatal("extra runtime file escaped the manifest")
	}
}
