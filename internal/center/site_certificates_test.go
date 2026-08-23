package center

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestSiteCertificateIsReusedAcrossFlatServiceHostnames(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	siteID := testSiteID(t, store)
	if _, err := store.UpdateSite(ctx, siteID, SiteInput{Name: "Home", Code: "home", Timezone: "UTC", DomainSuffix: "example.com"}); err != nil {
		t.Fatal(err)
	}
	configureCloudflareZoneForTest(t, store, "example.com")
	issued := 0
	var issuedNames []string
	store.issuePrivateCertificate = func(_ context.Context, dnsNames ...string) (managedCertificate, error) {
		issued++
		issuedNames = append([]string(nil), dnsNames...)
		return testManagedCertificate(t, dnsNames...), nil
	}
	if err := store.ensureSiteCertificateForHostname(ctx, siteID, "panel-3x-ui.home.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := store.ensureSiteCertificateForHostname(ctx, siteID, "subscription-3x-ui.home.example.com"); err != nil {
		t.Fatal(err)
	}
	if issued != 1 || !reflect.DeepEqual(issuedNames, []string{"*.home.example.com"}) {
		t.Fatalf("Site certificate issuance count=%d names=%#v", issued, issuedNames)
	}
	stored, err := store.storedSiteCertificate(ctx, siteID)
	if err != nil || stored.secretID == "" || !reflect.DeepEqual(stored.dnsNames, issuedNames) {
		t.Fatalf("stored Site certificate=%#v err=%v", stored, err)
	}
}

func TestSiteCertificateAddsParentWildcardForExistingNestedHostname(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	siteID := testSiteID(t, store)
	if _, err := store.UpdateSite(ctx, siteID, SiteInput{Name: "Home", Code: "home", Timezone: "UTC", DomainSuffix: "example.com"}); err != nil {
		t.Fatal(err)
	}
	configureCloudflareZoneForTest(t, store, "example.com")
	names, err := store.siteCertificateDNSNames(ctx, siteID, "panel.3x-ui.home.example.com")
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"*.3x-ui.home.example.com", "*.home.example.com"}
	if !reflect.DeepEqual(names, expected) {
		t.Fatalf("nested hostname certificate names=%#v, want %#v", names, expected)
	}
}

func TestSiteCertificateRejectsHostnameOutsideSiteNamespace(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	siteID := testSiteID(t, store)
	if _, err := store.UpdateSite(ctx, siteID, SiteInput{Name: "Home", Code: "home", Timezone: "UTC", DomainSuffix: "example.com"}); err != nil {
		t.Fatal(err)
	}
	configureCloudflareZoneForTest(t, store, "example.com")
	if _, err := store.siteCertificateDNSNames(ctx, siteID, "panel.other.example.com"); err == nil {
		t.Fatal("Site certificate unexpectedly accepted a sibling namespace")
	}
}

func configureCloudflareZoneForTest(t *testing.T, store *Store, zone string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(context.Background(), `INSERT INTO network_integrations(kind, mode, endpoint, status, created_at, updated_at)
		VALUES('cloudflare', 'oauth', ?, 'configured', ?, ?)`, zone, now, now); err != nil {
		t.Fatal(err)
	}
}
