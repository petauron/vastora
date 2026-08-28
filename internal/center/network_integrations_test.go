package center

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCloudflareAPIErrorsDoNotExposeToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret-cloudflare-token" {
			t.Fatalf("unexpected authorization header")
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusForbidden)
		_, _ = response.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"permission denied"}],"result":null}`))
	}))
	defer server.Close()
	client := cloudflareClient{accountID: "account", zoneID: "zone", token: "secret-cloudflare-token", baseURL: server.URL, http: server.Client()}
	_, err := client.createDNSRecord(context.Background(), "A", "app.example.com", "203.0.113.10", false)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("Cloudflare API error was not preserved: %v", err)
	}
	if strings.Contains(err.Error(), "secret-cloudflare-token") {
		t.Fatalf("Cloudflare token leaked in error: %v", err)
	}
}

func TestHeadscaleRequestKeepsFixedOriginAndRejectsRedirects(t *testing.T) {
	targetRequests := 0
	target := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		targetRequests++
		response.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Location", target.URL+"/stolen")
		response.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	client := headscaleClient{baseURL: redirect.URL, apiKey: "headscale-secret", http: redirect.Client()}
	if err := client.do(context.Background(), http.MethodGet, "/api/v1/user", nil, nil, nil); err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("Headscale redirect was followed or accepted: %v", err)
	}
	if targetRequests != 0 {
		t.Fatal("Headscale credentials were forwarded to a redirect target")
	}
	requestURL, err := headscaleRequestURL("https://headscale.example.test/", "/api/v1/user", url.Values{"name": []string{"vastora"}})
	if err != nil || requestURL != "https://headscale.example.test/api/v1/user?name=vastora" {
		t.Fatalf("unexpected validated Headscale URL: %q err=%v", requestURL, err)
	}
	for _, invalid := range []struct{ base, path string }{{"https://headscale.example.test/prefix", "/api/v1/user"}, {"https://headscale.example.test", "//metadata.invalid/"}, {"https://headscale.example.test", "/api/v1/user?next=https://metadata.invalid"}} {
		if _, err := headscaleRequestURL(invalid.base, invalid.path, nil); err == nil {
			t.Fatalf("unsafe Headscale request URL was accepted: %#v", invalid)
		}
	}
}

func TestHeadscaleEndpointMustBeOperatorAuthorized(t *testing.T) {
	store := &Store{headscaleAllowedEndpoints: []string{"https://headscale.example.test"}}
	endpoint, err := store.authorizedHeadscaleEndpoint("https://headscale.example.test/")
	if err != nil || endpoint != "https://headscale.example.test" {
		t.Fatalf("allowed Headscale endpoint was rejected: endpoint=%q err=%v", endpoint, err)
	}
	for _, value := range []string{"https://metadata.internal", "https://headscale.example.test/path", "http://headscale.example.test"} {
		if _, err := store.authorizedHeadscaleEndpoint(value); err == nil {
			t.Fatalf("unauthorized Headscale endpoint was accepted: %q", value)
		}
	}
}

func TestHeadscaleJoinKeyIsOneHourAndSingleUse(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/preauthkey" || request.Method != http.MethodPost {
			t.Fatalf("unexpected Headscale request: %s %s", request.Method, request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"preAuthKey":{"key":"one-time-key"}}`))
	}))
	defer server.Close()
	client := headscaleClient{baseURL: server.URL, apiKey: "headscale-secret", http: server.Client()}
	expires := time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC)
	key, err := client.createPreAuthKey(context.Background(), "42", []string{"tag:vastora-agent"}, expires)
	if err != nil {
		t.Fatal(err)
	}
	if key != "one-time-key" || body["user"] != "42" || body["reusable"] != false || body["expiration"] != expires.Format(time.RFC3339) {
		t.Fatalf("unexpected Headscale pre-auth key request: key=%q body=%#v", key, body)
	}
}

func TestHeadscaleBootstrapDoesNotRequireAnEnrolledAgent(t *testing.T) {
	var preAuthBody map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/user":
			_, _ = response.Write([]byte(`[{"id":"42","name":"vastora"}]`))
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/preauthkey":
			if err := json.NewDecoder(request.Body).Decode(&preAuthBody); err != nil {
				t.Fatal(err)
			}
			_, _ = response.Write([]byte(`{"preAuthKey":{"key":"bootstrap-one-time-key"}}`))
		default:
			t.Fatalf("unexpected Headscale request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	store, err := Open(t.TempDir(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.headscaleHTTPClient = server.Client()
	store.builtinHeadscaleDialAddress = server.Listener.Addr().String()
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	secretID, err := store.putSecret(context.Background(), tx, []byte("headscale-bootstrap-secret"), "integration:headscale")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`INSERT INTO network_integrations(kind, mode, endpoint, secret_id, status, created_at, updated_at) VALUES('headscale', 'external', ?, ?, 'configured', ?, ?)`, server.URL, secretID, now, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	join, err := store.CreateHeadscaleBootstrap(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if join.AgentID != "" || !strings.Contains(join.Command, "bootstrap-one-time-key") {
		t.Fatalf("unexpected bootstrap join: %#v", join)
	}
	tags, ok := preAuthBody["aclTags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "tag:vastora-agent" || tags[1] != "tag:vastora-gateway" {
		t.Fatalf("bootstrap tags = %#v", preAuthBody["aclTags"])
	}
	enrollment, err := store.CreateAgentEnrollment(context.Background(), AgentEnrollmentSpec{SiteID: testSiteID(t, store), Name: "private-node", CenterURL: "https://center.example.com", Gateway: true, UseHeadscale: true})
	if err != nil {
		t.Fatal(err)
	}
	if enrollment.InstallerURL != server.URL {
		t.Fatalf("installer URL = %q, want public Headscale endpoint %q", enrollment.InstallerURL, server.URL)
	}
	var bootstrapSecretID string
	var sealed []byte
	if err := store.db.QueryRow(`SELECT bootstrap_secret_id, sealed FROM agent_enrollment_tokens JOIN secrets ON secrets.id = bootstrap_secret_id WHERE token_hash = ?`, tokenHash(enrollment.Token)).Scan(&bootstrapSecretID, &sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "bootstrap-one-time-key") {
		t.Fatal("Headscale bootstrap key was stored in plaintext")
	}
	profile, err := store.AgentEnrollmentInstallProfile(context.Background(), enrollment.Token)
	if err != nil || !strings.Contains(profile.HeadscaleCommand, "bootstrap-one-time-key") || profile.HeadscaleURL != server.URL || len(profile.HeadscaleAddresses) != 0 {
		t.Fatalf("installer profile bootstrap = %#v, err = %v", profile, err)
	}
	if _, err := store.EnrollAgent(context.Background(), enrollment.Token, "test", "linux", "amd64"); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM secrets WHERE id = ?`, bootstrapSecretID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("consumed bootstrap secret count = %d, err = %v", remaining, err)
	}
}

func TestBuiltinHeadscaleIsolationUsesVerifiedPublicBinding(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO network_integrations(kind, mode, endpoint, status, created_at, updated_at) VALUES('headscale', 'builtin', 'https://headscale.example.com', 'configured', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)`, setupGatewayBindingSetting, `{"publicAddress":"203.0.113.10","bindAddress":"10.0.0.10"}`); err != nil {
		t.Fatal(err)
	}
	state, err := store.tailscaleIsolationDesiredState(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if state == nil || state.ControlURL != "https://headscale.example.com" || len(state.ControlAddresses) != 1 || state.ControlAddresses[0] != "203.0.113.10" {
		t.Fatalf("built-in Tailscale isolation state = %#v", state)
	}
}

func TestConfiguredIntegrationSecretCanBeRetained(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	secretID, err := store.putSecret(ctx, tx, []byte("existing-headscale-api-key"), "integration:headscale")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO network_integrations(kind, mode, endpoint, secret_id, status, created_at, updated_at) VALUES('headscale', 'builtin', 'https://headscale.example.com', ?, 'configured', ?, ?)`, secretID, now, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	gotID, gotValue, err := store.integrationSecret(ctx, "headscale")
	if err != nil {
		t.Fatal(err)
	}
	if gotID != secretID || gotValue != "existing-headscale-api-key" {
		t.Fatalf("stored integration secret was not retained: id=%q value=%q", gotID, gotValue)
	}
}
