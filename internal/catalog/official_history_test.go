package catalog

import (
	"os"
	"testing"
)

func TestOfficialPublicationHistoryRetainsRemovedVersions(t *testing.T) {
	raw, err := os.ReadFile("testdata/v3/valid-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := ParseCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ExtendOfficialManifestHistory(nil, value)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := ExtendOfficialManifestHistory(first, Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	value.Apps[0].Description.English += " altered"
	if _, err := ExtendOfficialManifestHistory(removed, value); err == nil {
		t.Fatal("removed version was republished with altered content")
	}
	value.Apps[0].Version = "99.0.0"
	if _, err := ExtendOfficialManifestHistory(removed, value); err != nil {
		t.Fatal(err)
	}
}
