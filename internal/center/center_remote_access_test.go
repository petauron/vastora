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
			if body["domain"] != "center-vastora.example.com" || !strings.Contains(mustJSONString(t, body["policies"]), `"email_domain":{"domain":"example.org"}`) {
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
			encoded := mustJSONString(t, body)
			if !strings.Contains(encoded, `"hostname":"center-vastora.example.com"`) || !strings.Contains(encoded, `"service":"http://vastora-center:8080"`) {
				t.Fatalf("Tunnel does not target the private bridge alias: %#v", body)
			}
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/zones/zone/dns_records":
			if request.URL.Query().Get("name") != "center-vastora.example.com" {
				t.Fatalf("unexpected DNS lookup: %s", request.URL.String())
			}
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
		case request.Method == http.MethodPost && request.URL.Path == "/zones/zone/dns_records":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["name"] != "center-vastora.example.com" || body["proxied"] != true {
				t.Fatalf("unexpected remote DNS record: %#v", body)
			}
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
	if !view.Enabled || view.Status != "configured" || view.Hostname != "center-vastora.example.com" || infrastructure.remoteAccess.Token != "cloudflare-tunnel-token-value" {
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

func TestCenterRemoteAccessCreatesTurnstileBeforePublishingDirectTunnel(t *testing.T) {
	operations := []string{}
	cloudflare := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		operations = append(operations, request.Method+" "+request.URL.Path)
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/accounts/account/cfd_tunnel":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
		case request.Method == http.MethodGet && request.URL.Path == "/zones/zone":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{"name":"example.com"}}`))
		case request.Method == http.MethodPost && request.URL.Path == "/accounts/account/challenges/widgets":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["mode"] != "managed" || !strings.Contains(mustJSONString(t, body["domains"]), "center-vastora.example.com") {
				t.Fatalf("unexpected Turnstile widget: %#v", body)
			}
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{"sitekey":"turnstile-site-key","secret":"turnstile-secret"}}`))
		case request.Method == http.MethodPost && request.URL.Path == "/accounts/account/cfd_tunnel":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{"id":"tunnel-id"}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/accounts/account/cfd_tunnel/tunnel-id/token":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":"cloudflare-tunnel-token-value"}`))
		case request.Method == http.MethodPut && request.URL.Path == "/accounts/account/cfd_tunnel/tunnel-id/configurations":
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
	storeCloudflareOAuthIntegration(t, store, cloudflareOAuthToken{AccessToken: "access", RefreshToken: "refresh", Scope: "zone.read dns.write argotunnel.write turnstile.write", ExpiresAt: time.Now().Add(time.Hour)})
	infrastructure := &fakeBuiltinHeadscaleInstaller{}
	server := NewServer(store, "", false).WithInfrastructureManager(infrastructure)
	view, err := server.ConfigureCenterRemoteAccess(context.Background(), CenterRemoteAccessInput{Enabled: true, ProtectionMode: "native"}, "https://center.vastora.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Enabled || view.ProtectionMode != "native" || view.TurnstileSiteKey != "turnstile-site-key" || view.AudienceKind != "" || view.AudienceValue != "" {
		t.Fatalf("unexpected native remote access result: %#v", view)
	}
	turnstileIndex, dnsIndex := operationIndex(operations, "POST /accounts/account/challenges/widgets"), operationIndex(operations, "POST /zones/zone/dns_records")
	if turnstileIndex < 0 || dnsIndex < 0 || turnstileIndex >= dnsIndex {
		t.Fatalf("DNS was published before Turnstile: %v", operations)
	}
	var secretID string
	if err := store.db.QueryRow(`SELECT COALESCE(turnstile_secret_id, '') FROM center_remote_access WHERE id = 1`).Scan(&secretID); err != nil || secretID == "" {
		t.Fatalf("Turnstile secret was not stored by reference: id=%q err=%v", secretID, err)
	}
	view, err = server.ConfigureCenterRemoteAccess(context.Background(), CenterRemoteAccessInput{Enabled: false}, "https://center.vastora.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if view.Enabled || operationIndex(operations, "DELETE /accounts/account/challenges/widgets/turnstile-site-key") < 0 {
		t.Fatalf("native remote access was not fully disabled: view=%#v operations=%v", view, operations)
	}
}

func TestNormalizeCenterRemoteAccessUsesUniversalSSLCompatibleHostname(t *testing.T) {
	input, hostname, err := normalizeCenterRemoteAccess(CenterRemoteAccessInput{Enabled: true, AudienceKind: "email", AudienceValue: "admin@example.com"}, "https://center.vastora.example.com", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if hostname != "center-vastora.example.com" || input.AudienceValue != "admin@example.com" {
		t.Fatalf("unexpected normalized remote access: hostname=%q input=%#v", hostname, input)
	}
}

func TestCenterRemoteAccessMigratesLegacyNestedHostname(t *testing.T) {
	operations := []string{}
	cloudflare := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		operations = append(operations, request.Method+" "+request.URL.Path)
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/zones/zone":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{"name":"example.com"}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/accounts/account/access/organizations":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{"auth_domain":"vastora-account.cloudflareaccess.com"}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/accounts/account/access/identity_providers":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":[{"id":"otp-id","type":"onetimepin"}]}`))
		case request.Method == http.MethodPost && request.URL.Path == "/accounts/account/access/apps":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{"id":"new-access-app"}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/accounts/account/cfd_tunnel":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
		case request.Method == http.MethodPost && request.URL.Path == "/accounts/account/cfd_tunnel":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{"id":"new-tunnel"}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/accounts/account/cfd_tunnel/new-tunnel/token":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":"new-token"}`))
		case request.Method == http.MethodPut && request.URL.Path == "/accounts/account/cfd_tunnel/new-tunnel/configurations":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/zones/zone/dns_records":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
		case request.Method == http.MethodPost && request.URL.Path == "/zones/zone/dns_records":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{"id":"new-dns"}}`))
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
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO center_remote_access(id, hostname, audience_kind, audience_value, otp_identity_provider_id, access_application_id, tunnel_id, dns_record_id, status, created_at, updated_at)
		VALUES(1, 'center.vastora.example.com', 'email_domain', 'example.org', 'old-otp', 'old-access-app', 'old-tunnel', 'old-dns', 'configured', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	infrastructure := &fakeBuiltinHeadscaleInstaller{}
	view, err := NewServer(store, "", false).WithInfrastructureManager(infrastructure).ConfigureCenterRemoteAccess(context.Background(), CenterRemoteAccessInput{Enabled: true, AudienceKind: "email_domain", AudienceValue: "example.org"}, "https://center.vastora.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if view.Hostname != "center-vastora.example.com" || !view.Enabled || infrastructure.remoteAccess.Token != "new-token" {
		t.Fatalf("legacy remote access was not migrated: view=%#v runtime=%#v", view, infrastructure.remoteAccess)
	}
	for _, removed := range []string{
		"DELETE /zones/zone/dns_records/old-dns",
		"DELETE /accounts/account/access/apps/old-access-app",
		"DELETE /accounts/account/cfd_tunnel/old-tunnel",
	} {
		if operationIndex(operations, removed) < 0 {
			t.Fatalf("legacy resource was not removed (%s): %v", removed, operations)
		}
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

func TestCloudflareIntegrationReportsLoginProtectionCapabilities(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	storeCloudflareOAuthIntegration(t, store, cloudflareOAuthToken{AccessToken: "access", RefreshToken: "refresh", Scope: "zone.read access.write access-acct.write turnstile.write", ExpiresAt: time.Now().Add(time.Hour)})
	view, err := store.Integration(context.Background(), "cloudflare")
	if err != nil {
		t.Fatal(err)
	}
	if !view.AccessManagement || !view.TurnstileManagement {
		t.Fatalf("login protection capabilities were not reported: %#v", view)
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
