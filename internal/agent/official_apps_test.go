package agent

import (
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/catalog"
)

func TestValidateOfficialCatalogRejectsVersionDrift(t *testing.T) {
	apps := make([]catalog.AppManifest, 0, len(officialAppVersions))
	for id, version := range officialAppVersions {
		apps = append(apps, catalog.AppManifest{ID: id, Version: version})
	}
	if err := ValidateOfficialCatalog(catalog.Catalog{Apps: apps}); err != nil {
		t.Fatalf("validate official catalog: %v", err)
	}

	apps[0].Version += "-drifted"
	if err := ValidateOfficialCatalog(catalog.Catalog{Apps: apps}); err == nil || !strings.Contains(err.Error(), "requires version") {
		t.Fatalf("version drift error = %v", err)
	}
}

func TestValidateOfficialCatalogRequiresEachAppExactlyOnce(t *testing.T) {
	apps := make([]catalog.AppManifest, 0, len(officialAppVersions))
	for id, version := range officialAppVersions {
		apps = append(apps, catalog.AppManifest{ID: id, Version: version})
	}

	missing := catalog.Catalog{Apps: append([]catalog.AppManifest(nil), apps[1:]...)}
	if err := ValidateOfficialCatalog(missing); err == nil || !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("missing app error = %v", err)
	}

	duplicated := catalog.Catalog{Apps: append(append([]catalog.AppManifest(nil), apps...), apps[0])}
	if err := ValidateOfficialCatalog(duplicated); err == nil || !strings.Contains(err.Error(), "is duplicated") {
		t.Fatalf("duplicate app error = %v", err)
	}
}
