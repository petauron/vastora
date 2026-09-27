package center

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/petauron/catalog/catalog"
)

// Only historical lifecycle/migration tests may install the archived alpha.181
// manifest. It is never embedded in Center or added to the production catalog.
func openLegacyOrchestrationStore(t *testing.T) *Store {
	t.Helper()
	store := openOrchestrationStore(t)
	seedLegacyProxyManifest(t, store)
	return store
}

func seedLegacyProxyManifest(t *testing.T, store *Store) {
	t.Helper()
	value, _, err := readAcceptedOfficialCatalog(context.Background(), store.db, "stable")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/legacy-proxy-alpha181.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest catalog.AppManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	// Product-orchestration simulation only: the archived bytes above remain
	// untouched for migration audits. This new test recipe is not an assertion
	// that a legacy production installation was adopted or can run unmodified.
	manifest.PackageRevision = 1
	manifest.Runtime = &catalog.RuntimeSpec{Kind: "docker", Version: 1, Docker: &catalog.DockerRuntime{}}
	for _, image := range manifest.Images {
		manifest.Runtime.Docker.Containers = append(manifest.Runtime.Docker.Containers, catalog.Container{Name: image.Name, Image: image.Name})
	}
	value.Apps = append(value.Apps, manifest)
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SeedOfficialCatalog(context.Background(), encoded); err != nil {
		t.Fatal(err)
	}
}
