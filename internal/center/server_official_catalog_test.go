package center

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOfficialEndpointWithoutTrustedTargetIsUnavailable(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := NewServer(store, "", false)
	response := httptest.NewRecorder()
	server.handleOfficialCatalog(response, httptest.NewRequest(http.MethodGet, "/api/v1/catalog/official", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing catalog returned %d: %s", response.Code, response.Body.String())
	}
}

func TestOfficialEndpointRejectsModifiedCachedBytes(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	if _, err := store.db.Exec(`UPDATE official_catalog_trust SET target = X'7b7d' WHERE channel = 'stable'`); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	NewServer(store, "", false).handleOfficialCatalog(response, httptest.NewRequest(http.MethodGet, "/api/v1/catalog/official", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("modified cache returned %d", response.Code)
	}
}

func TestOfficialRefreshWithoutRootRecordsFailureWithoutAuthorizingApps(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.ConfigureOfficialCatalog(ctx, "https://example.invalid/catalog"); err != nil {
		t.Fatal(err)
	}
	server := NewServer(store, "", false).WithOfficialCatalogTrust("https://example.invalid/catalog", nil)
	if _, err := server.RefreshCatalogSource(ctx, OfficialCatalogSourceID); err == nil {
		t.Fatal("refresh without independent root succeeded")
	}
	sources, err := store.ListSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Status != "failed" || sources[0].LastError == "" {
		t.Fatalf("missing failure state: %+v", sources)
	}
	apps, err := store.ListApps(ctx)
	if err != nil || len(apps) != 0 {
		t.Fatalf("unverified apps became available: %+v %v", apps, err)
	}
}
