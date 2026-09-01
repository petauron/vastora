package center

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExternalHelpersAreDisabledByDefault(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime, err := store.ConfigureExternalHelpers(ExternalHelperConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.ReleaseChecker != nil || runtime.ReleaseInstallerBaseURL != "" || runtime.PublicAddressLookupURL != "" || store.lookupPublicAddress != nil || store.verifyPublicEntry != nil || store.lookupPublicRegion != nil || store.CloudflareOAuthAvailable() {
		t.Fatalf("disabled external helpers were enabled: %#v", runtime)
	}
}

func TestExternalHelperConfigurationIsCompleteAndHTTPS(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, config := range []ExternalHelperConfig{
		{ReleaseMetadataURL: "https://releases.example.com/install.sh"},
		{CloudflareOAuthClientID: "client"},
		{PublicHelperOrigin: "http://helper.example.com"},
		{RegionLookupURL: "https://user:secret@helper.example.com"},
	} {
		if _, err := store.ConfigureExternalHelpers(config); err == nil {
			t.Fatalf("invalid external helper configuration was accepted: %#v", config)
		}
	}
}

func TestExternalHelpersAcceptExplicitPrivateHTTPSServices(t *testing.T) {
	helper := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer helper.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runtime, err := store.ConfigureExternalHelpers(ExternalHelperConfig{
		ReleaseMetadataURL:         helper.URL + "/install.sh",
		ReleaseInstallerBaseURL:    helper.URL + "/releases/",
		PublicHelperOrigin:         helper.URL,
		RegionLookupURL:            helper.URL + "/regions/",
		CloudflareOAuthClientID:    "private-client",
		CloudflareOAuthRedirectURL: helper.URL + "/oauth/callback",
		CloudflareOAuthRelayURL:    helper.URL + "/oauth/relay/",
		AllowPrivate:               true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.ReleaseChecker == nil || runtime.ReleaseInstallerBaseURL != helper.URL+"/releases" || runtime.PublicAddressLookupURL != helper.URL+"/network/public-address" || !runtime.PublicHelperAllowPrivate || store.lookupPublicAddress == nil || store.verifyPublicEntry == nil || store.lookupPublicRegion == nil || !store.CloudflareOAuthAvailable() {
		t.Fatalf("external helper configuration is incomplete: %#v", runtime)
	}
}

func TestExternalHelperAddressValidationRejectsMetadataAndPrivateByDefault(t *testing.T) {
	for _, raw := range []string{"169.254.169.254", "100.100.100.200", "127.0.0.1", "10.0.0.1", "100.64.0.1", "192.0.2.1", "198.18.0.1"} {
		if err := validateExternalHelperIP(net.ParseIP(raw), false); err == nil {
			t.Fatalf("restricted address %s was accepted", raw)
		}
	}
	if err := validateExternalHelperIP(net.ParseIP("127.0.0.1"), true); err != nil {
		t.Fatalf("explicit private helper was rejected: %v", err)
	}
}

func TestDisabledRegionHelperFailsBeforeAnyRequest(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SuggestAgentRegion(context.Background(), "missing-agent"); err == nil {
		t.Fatal("disabled region lookup was accepted")
	}
}
