package center

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCenterRemoteAccessCreatesAccessBeforePublishingDNS(t *testing.T) {
	operations := []string{}
	cloudflare := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		operations = append(operations, request.Method+" "+request.URL.Path)
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/accounts/account/cfd_tunnel":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
		case request.Method == http.MethodGet && request.URL.Path == "/zones/zone":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{"name":"example.com"}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/accounts/account/access/organizations":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{"auth_domain":"vastora-account.cloudflareaccess.com"}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/accounts/account/access/identity_providers":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
		case request.Method == http.MethodPost && request.URL.Path == "/accounts/account/access/identity_providers":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{"id":"otp-id","type":"onetimepin"}}`))
		case request.Method == http.MethodPost && request.URL.Path == "/accounts/account/access/apps":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["domain"] != "center.vastora.example.com" || !strings.Contains(mustJSONString(t, body["policies"]), `"email_domain":{"domain":"example.org"}`) {
				t.Fatalf("unexpected Access application: %#v", body)
			}
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{"id":"access-app"}}`))
		case request.Method == http.MethodPost && request.URL.Path == "/accounts/account/cfd_tunnel":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{"id":"tunnel-id"}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/accounts/account/cfd_tunnel/tunnel-id/token":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":"cloudflare-tunnel-token-value"}`))
		case request.Method == http.MethodPut && request.URL.Path == "/accounts/account/cfd_tunnel/tunnel-id/configurations":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(mustJSONString(t, body), `"service":"http://vastora-center:8080"`) {
				t.Fatalf("Tunnel does not target the private bridge alias: %#v", body)
			}
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/zones/zone/dns_records":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
		case request.Method == http.MethodPost && request.URL.Path == "/zones/zone/dns_records":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{"id":"dns-id"}}`))
		case request.Method == http.MethodDelete:
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{}}`))
		default:
			t.Fatalf("unexpected Cloudflare request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer cloudflare.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.cloudflareOAuth = cloudflareOAuthConfig{APIURL: cloudflare.URL, HTTPClient: cloudflare.Client()}
	storeCloudflareOAuthIntegration(t, store, cloudflareOAuthToken{AccessToken: "access", RefreshToken: "refresh", Scope: "zone.read dns.write argotunnel.write access.write access-acct.write", ExpiresAt: time.Now().Add(time.Hour)})
	infrastructure := &fakeBuiltinHeadscaleInstaller{}
	server := NewServer(store, "", false).WithInfrastructureManager(infrastructure)
	view, err := server.ConfigureCenterRemoteAccess(context.Background(), CenterRemoteAccessInput{Enabled: true, AudienceKind: "email_domain", AudienceValue: "@example.org"}, "https://center.vastora.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Enabled || view.Status != "configured" || infrastructure.remoteAccess.Token != "cloudflare-tunnel-token-value" {
		t.Fatalf("unexpected remote access result: %#v runtime=%#v", view, infrastructure.remoteAccess)
	}
	accessIndex, dnsIndex := operationIndex(operations, "POST /accounts/account/access/apps"), operationIndex(operations, "POST /zones/zone/dns_records")
	if accessIndex < 0 || dnsIndex < 0 || accessIndex >= dnsIndex {
		t.Fatalf("DNS was published before Access: %v", operations)
	}
	view, err = server.ConfigureCenterRemoteAccess(context.Background(), CenterRemoteAccessInput{Enabled: false}, "https://center.vastora.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if view.Enabled || view.Status != "disabled" || infrastructure.remoteAccess.Enabled {
		t.Fatalf("remote access was not disabled: %#v runtime=%#v", view, infrastructure.remoteAccess)
	}
}

func TestCenterRemoteAccessRequiresAccessOAuthScope(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	storeCloudflareOAuthIntegration(t, store, cloudflareOAuthToken{AccessToken: "access", RefreshToken: "refresh", Scope: "zone.read dns.write argotunnel.write access.write", ExpiresAt: time.Now().Add(time.Hour)})
	server := NewServer(store, "", false).WithInfrastructureManager(&fakeBuiltinHeadscaleInstaller{})
	_, err = server.ConfigureCenterRemoteAccess(context.Background(), CenterRemoteAccessInput{Enabled: true, AudienceKind: "email", AudienceValue: "admin@example.com"}, "https://center.vastora.example.com")
	if err == nil || !strings.Contains(err.Error(), "reconnect Cloudflare") {
		t.Fatalf("unexpected scope error: %v", err)
	}
	var rows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM center_remote_access`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("remote access was persisted before scope validation: rows=%d err=%v", rows, err)
	}
}

func TestDisabledCenterRemoteAccessDoesNotRequireDeploymentHelper(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	view, err := NewServer(store, "", false).ConfigureCenterRemoteAccess(context.Background(), CenterRemoteAccessInput{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if view.Available || view.Enabled || view.Status != "disabled" {
		t.Fatalf("unexpected disabled remote access view: %#v", view)
	}
}

func TestCloudflareIntegrationReportsAccessManagementCapability(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	storeCloudflareOAuthIntegration(t, store, cloudflareOAuthToken{AccessToken: "access", RefreshToken: "refresh", Scope: "zone.read access.write access-acct.write", ExpiresAt: time.Now().Add(time.Hour)})
	view, err := store.Integration(context.Background(), "cloudflare")
	if err != nil {
		t.Fatal(err)
	}
	if !view.AccessManagement {
		t.Fatalf("Access management capability was not reported: %#v", view)
	}
}

func TestEnsureAccessOrganizationCreatesMissingOrganization(t *testing.T) {
	created := false
	cloudflare := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case http.MethodGet:
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(`{"success":false,"errors":[{"code":1001,"message":"organization not found"}],"result":null}`))
		case http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["auth_domain"] != "vastora-account.cloudflareaccess.com" || body["name"] != "Vastora" {
				t.Fatalf("unexpected Access organization: %#v", body)
			}
			created = true
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{"auth_domain":"vastora-account.cloudflareaccess.com"}}`))
		default:
			t.Fatalf("unexpected Access organization request: %s", request.Method)
		}
	}))
	defer cloudflare.Close()

	client := cloudflareClient{accountID: "account", token: "token", baseURL: cloudflare.URL, http: cloudflare.Client()}
	if err := client.ensureAccessOrganization(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("missing Access organization was not created")
	}
}

func mustJSONString(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func operationIndex(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}
