package center

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/petauron/vastora/internal/catalog"
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
	value.Apps = append(value.Apps, manifest)
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SeedOfficialCatalog(context.Background(), encoded); err != nil {
		t.Fatal(err)
	}
}
