package center

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestCloudflareOAuthStartUsesPKCEWithoutExposingSecrets(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixed := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return fixed }
	store.cloudflareOAuth.ClientID = "oauth-client"
	store.cloudflareOAuth.AuthorizationURL = "https://dash.cloudflare.test/oauth2/auth"

	started, err := store.StartCloudflareOAuth()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(started.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	store.cloudflareOAuthMu.Lock()
	session := store.cloudflareOAuthSessions[started.SessionID]
	store.cloudflareOAuthMu.Unlock()
	if session == nil {
		t.Fatal("OAuth session was not saved")
	}
	query := parsed.Query()
	if query.Get("client_id") != "oauth-client" || query.Get("redirect_uri") != cloudflareOAuthRedirectURI || query.Get("response_type") != "code" {
		t.Fatalf("unexpected authorization URL: %s", started.AuthorizationURL)
	}
	if query.Get("state") != session.State || query.Get("code_challenge") != oauthSHA256(session.PKCEVerifier) || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("OAuth request did not bind state and PKCE: %s", started.AuthorizationURL)
	}
	if query.Get("scope") != "zone.read dns.write argotunnel.write offline_access" {
		t.Fatalf("OAuth request used unexpected scopes: %q", query.Get("scope"))
	}
	for _, secret := range []string{session.PollSecret, session.PKCEVerifier} {
		if strings.Contains(started.AuthorizationURL, secret) {
			t.Fatal("OAuth authorization URL exposed a Center-only secret")
		}
	}
	if started.ExpiresAt != fixed.Add(cloudflareOAuthLifetime) {
		t.Fatalf("OAuth expiry = %s", started.ExpiresAt)
	}
}

func TestCloudflareOAuthPollAndCompleteStoresEncryptedToken(t *testing.T) {
	var expectedPollSecret, expectedVerifier string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(request.URL.Path, "/relay/sessions/"):
			if request.Header.Get("Authorization") != "Bearer "+expectedPollSecret {
				t.Fatalf("unexpected relay authorization")
			}
			_, _ = response.Write([]byte(`{"status":"authorized","code":"authorization-code"}`))
		case request.URL.Path == "/token":
			if err := request.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if request.Form.Get("grant_type") != "authorization_code" || request.Form.Get("code") != "authorization-code" || request.Form.Get("code_verifier") != expectedVerifier {
				t.Fatalf("unexpected token exchange: %#v", request.Form)
			}
			_, _ = response.Write([]byte(`{"access_token":"access-secret","refresh_token":"refresh-secret","token_type":"bearer","scope":"zone.read","expires_in":3600}`))
		case request.URL.Path == "/zones":
			if request.Header.Get("Authorization") != "Bearer access-secret" {
				t.Fatalf("unexpected zone authorization")
			}
			_, _ = response.Write([]byte(`{"success":true,"errors":[],"result":[{"id":"zone","name":"example.com","account":{"id":"account","name":"Example"}}],"result_info":{"total_pages":1}}`))
		case request.URL.Path == "/accounts/account/cfd_tunnel":
			_, _ = response.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
		case request.URL.Path == "/zones/zone":
			_, _ = response.Write([]byte(`{"success":true,"errors":[],"result":{"name":"example.com"}}`))
		default:
			t.Fatalf("unexpected Cloudflare request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.cloudflareOAuth = cloudflareOAuthConfig{ClientID: "oauth-client", AuthorizationURL: server.URL + "/auth", TokenURL: server.URL + "/token", RelayURL: server.URL + "/relay", APIURL: server.URL, HTTPClient: server.Client()}
	started, err := store.StartCloudflareOAuth()
	if err != nil {
		t.Fatal(err)
	}
	store.cloudflareOAuthMu.Lock()
	expectedPollSecret = store.cloudflareOAuthSessions[started.SessionID].PollSecret
	expectedVerifier = store.cloudflareOAuthSessions[started.SessionID].PKCEVerifier
	store.cloudflareOAuthMu.Unlock()

	poll, err := store.PollCloudflareOAuth(context.Background(), started.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if poll.Status != "authorized" || len(poll.Zones) != 1 || poll.Zones[0].ID != "zone" {
		t.Fatalf("unexpected OAuth poll result: %#v", poll)
	}
	integration, err := store.CompleteCloudflareOAuth(context.Background(), started.SessionID, "zone")
	if err != nil {
		t.Fatal(err)
	}
	if integration.Mode != "oauth" || integration.Endpoint != "example.com" || !integration.SecretSet {
		t.Fatalf("unexpected Cloudflare integration: %#v", integration)
	}
	var sealed []byte
	if err := store.db.QueryRow(`SELECT sealed FROM secrets WHERE id = (SELECT secret_id FROM network_integrations WHERE kind = 'cloudflare')`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "access-secret") || strings.Contains(string(sealed), "refresh-secret") {
		t.Fatal("Cloudflare OAuth token was stored in plaintext")
	}
	if _, ok := store.cloudflareOAuthSessions[started.SessionID]; ok {
		t.Fatal("completed OAuth session was retained")
	}
}

func TestConfigureSetupDNSRollsBackNewRecordsOnConflict(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/zones/zone/dns_records" && request.URL.Query().Get("name") == "center.example.com":
			_, _ = response.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
		case request.Method == http.MethodPost && request.URL.Path == "/zones/zone/dns_records":
			body, _ := io.ReadAll(request.Body)
			if !strings.Contains(string(body), `"proxied":false`) {
				t.Fatalf("setup DNS record was unexpectedly proxied: %s", body)
			}
			_, _ = response.Write([]byte(`{"success":true,"errors":[],"result":{"id":"created-record"}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/zones/zone/dns_records" && request.URL.Query().Get("name") == "headscale.example.com":
			_, _ = response.Write([]byte(`{"success":true,"errors":[],"result":[{"id":"existing","type":"A","name":"headscale.example.com","content":"203.0.113.99","proxied":false}]}`))
		case request.Method == http.MethodDelete && request.URL.Path == "/zones/zone/dns_records/created-record":
			deleted = true
			_, _ = response.Write([]byte(`{"success":true,"errors":[],"result":{}}`))
		default:
			t.Fatalf("unexpected DNS request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.cloudflareOAuth = cloudflareOAuthConfig{ClientID: "oauth-client", APIURL: server.URL, HTTPClient: server.Client()}
	storeCloudflareOAuthIntegration(t, store, cloudflareOAuthToken{AccessToken: "access-secret", RefreshToken: "refresh-secret", ExpiresAt: time.Now().Add(time.Hour)})
	candidates := []networking.Candidate{{Address: "203.0.113.10", Interface: "eth0", Family: "ipv4", Kind: networking.KindPublic}}
	_, err = store.ConfigureSetupDNS(context.Background(), SetupDNSInput{CenterURL: "https://center.example.com:8443", HeadscaleURL: "https://headscale.example.com:8443", PublicAddress: "203.0.113.10"}, candidates)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("conflicting DNS record was accepted: %v", err)
	}
	if !deleted {
		t.Fatal("new DNS record was not rolled back after a later conflict")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'cloudflare_setup_dns_records'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed DNS setup was persisted: count=%d err=%v", count, err)
	}
}

func TestConfigureSetupDNSRejectsAnUnreportedPublicAddress(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.ConfigureSetupDNS(context.Background(), SetupDNSInput{CenterURL: "https://center.example.com:8443", PublicAddress: "203.0.113.10"}, nil)
	if err == nil || !strings.Contains(err.Error(), "not assigned") {
		t.Fatalf("unreported public address was accepted: %v", err)
	}
}

func storeCloudflareOAuthIntegration(t *testing.T, store *Store, token cloudflareOAuthToken) {
	t.Helper()
	encoded, err := json.Marshal(token)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	secretID, err := store.putSecret(context.Background(), tx, encoded, "integration:cloudflare")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`INSERT INTO network_integrations(kind, mode, endpoint, account_id, zone_id, secret_id, status, created_at, updated_at) VALUES('cloudflare', 'oauth', 'example.com', 'account', 'zone', ?, 'configured', ?, ?)`, secretID, now, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
