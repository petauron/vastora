package center

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestStaticHandlerKeepsRequestsInsideStaticRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	staticDir := filepath.Join(root, "web")
	if err := os.Mkdir(staticDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("spa-index"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "asset.txt"), []byte("public-asset"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("outside-secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	handler := (&Server{staticDir: staticDir}).staticHandler()
	for _, test := range []struct {
		name       string
		target     string
		wantBody   string
		forbidBody string
		wantCache  string
	}{
		{name: "existing asset", target: "http://example.test/asset.txt", wantBody: "public-asset", wantCache: "no-store"},
		{name: "SPA fallback", target: "http://example.test/dashboard/settings", wantBody: "spa-index", wantCache: "no-store"},
		{name: "path traversal", target: "http://example.test/%2e%2e/secret.txt", forbidBody: "outside-secret", wantCache: "no-store"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body, err := io.ReadAll(response.Result().Body)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantBody != "" && string(body) != test.wantBody {
				t.Fatalf("got body %q, want %q", body, test.wantBody)
			}
			if test.forbidBody != "" && strings.Contains(string(body), test.forbidBody) {
				t.Fatalf("response exposed a file outside the static root: %q", body)
			}
			if cache := response.Header().Get("Cache-Control"); cache != test.wantCache {
				t.Fatalf("Cache-Control = %q, want %q", cache, test.wantCache)
			}
		})
	}

	assetDir := filepath.Join(staticDir, "assets")
	if err := os.Mkdir(assetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetDir, "app-hash.js"), []byte("hashed-asset"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://example.test/assets/app-hash.js", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if cache := response.Header().Get("Cache-Control"); cache != "public, max-age=31536000, immutable" {
		t.Fatalf("hashed asset Cache-Control = %q", cache)
	}
}

func TestCredentialsCanCreateOnlyOneAdministrator(t *testing.T) {
	t.Parallel()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := httptest.NewServer(NewServer(store, "", false).Handler())
	defer server.Close()
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "correct-horse-battery-staple"})
	response, err := http.Post(server.URL+"/api/v1/setup/admin", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("got status %d", response.StatusCode)
	}
	response.Body.Close()
	response, err = http.Post(server.URL+"/api/v1/setup/admin", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("got status %d, want %d", response.StatusCode, http.StatusBadRequest)
	}

	wrongLogin, _ := json.Marshal(map[string]string{"username": "other", "password": "correct-horse-battery-staple"})
	response, err = http.Post(server.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(wrongLogin))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong username got status %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}

	response, err = http.Post(server.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("valid credentials got status %d, want %d", response.StatusCode, http.StatusOK)
	}
}

func TestAuthenticationCookiesFollowDirectAndProxiedHTTPS(t *testing.T) {
	for _, test := range []struct {
		name           string
		directTLS      bool
		forwardedProto string
		wantSecure     bool
	}{
		{name: "direct HTTP"},
		{name: "direct TLS cannot be downgraded by proxy header", directTLS: true, forwardedProto: "http", wantSecure: true},
		{name: "HTTPS reverse proxy", forwardedProto: "https", wantSecure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			handler := NewServer(store, "", test.directTLS).Handler()
			body, _ := json.Marshal(map[string]string{"username": "admin", "password": "correct-horse-battery-staple"})
			request := httptest.NewRequest(http.MethodPost, "/api/v1/setup/admin", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Forwarded-Proto", test.forwardedProto)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusCreated {
				t.Fatalf("setup status = %d, body = %q", response.Code, response.Body.String())
			}
			cookies := assertAuthenticationCookies(t, response.Result().Cookies(), test.wantSecure, false)

			request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
			request.Header.Set("X-CSRF-Token", cookies["vastora_csrf"].Value)
			request.Header.Set("X-Forwarded-Proto", test.forwardedProto)
			request.AddCookie(cookies["vastora_session"])
			response = httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("logout status = %d, body = %q", response.Code, response.Body.String())
			}
			assertAuthenticationCookies(t, response.Result().Cookies(), test.wantSecure, true)

			request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Forwarded-Proto", test.forwardedProto)
			response = httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("login status = %d, body = %q", response.Code, response.Body.String())
			}
			assertAuthenticationCookies(t, response.Result().Cookies(), test.wantSecure, false)
		})
	}
}

func assertAuthenticationCookies(t *testing.T, cookies []*http.Cookie, secure, cleared bool) map[string]*http.Cookie {
	t.Helper()
	if len(cookies) != 2 {
		t.Fatalf("authentication cookies = %#v", cookies)
	}
	values := make(map[string]*http.Cookie, len(cookies))
	for _, cookie := range cookies {
		values[cookie.Name] = cookie
	}
	session, csrf := values["vastora_session"], values["vastora_csrf"]
	if session == nil || !session.HttpOnly || session.Secure != secure || session.SameSite != http.SameSiteStrictMode || session.Path != "/" {
		t.Fatalf("unsafe session cookie = %#v", session)
	}
	if csrf == nil || csrf.HttpOnly || csrf.Secure != secure || csrf.SameSite != http.SameSiteStrictMode || csrf.Path != "/" {
		t.Fatalf("unsafe CSRF cookie = %#v", csrf)
	}
	if cleared {
		if session.MaxAge >= 0 || csrf.MaxAge >= 0 || session.Value != "" || csrf.Value != "" {
			t.Fatalf("cookies were not cleared: session=%#v csrf=%#v", session, csrf)
		}
	} else if session.Expires.IsZero() || csrf.Expires.IsZero() {
		t.Fatalf("cookies do not expire: session=%#v csrf=%#v", session, csrf)
	}
	return values
}

func TestProtectedRoutesRejectInvalidExpiredAndMissingCSRFSessions(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixed := time.Date(2026, time.August, 30, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return fixed }
	session, csrf, err := store.CreateFirstAdmin(context.Background(), "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(store, "", false).Handler()

	invalid := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/sources", nil)
	invalid.AddCookie(&http.Cookie{Name: "vastora_session", Value: "invalid-session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, invalid)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid session status = %d, body = %q", response.Code, response.Body.String())
	}

	missingCSRF := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader("{}"))
	missingCSRF.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, missingCSRF)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing CSRF status = %d, body = %q", response.Code, response.Body.String())
	}
	if err := store.ValidateSession(context.Background(), session, csrf, false); err != nil {
		t.Fatalf("CSRF rejection revoked the valid session: %v", err)
	}
	wrongCSRF := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	wrongCSRF.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
	wrongCSRF.Header.Set("X-CSRF-Token", "wrong-csrf-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, wrongCSRF)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong CSRF status = %d, body = %q", response.Code, response.Body.String())
	}
	if err := store.ValidateSession(context.Background(), session, csrf, false); err != nil {
		t.Fatalf("wrong CSRF rejection revoked the valid session: %v", err)
	}

	store.now = func() time.Time { return fixed.Add(sessionLifetime + time.Second) }
	expired := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/sources", nil)
	expired.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, expired)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "expired") {
		t.Fatalf("expired session status = %d, body = %q", response.Code, response.Body.String())
	}
}

func TestLogoutRevokesServerSideSession(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	session, csrf, err := store.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(store, "", false)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader("{}"))
	request.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
	request.AddCookie(&http.Cookie{Name: "vastora_csrf", Value: csrf})
	request.Header.Set("X-CSRF-Token", csrf)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("logout status = %d body=%s", response.Code, response.Body.String())
	}
	if err := store.ValidateSession(ctx, session, csrf, false); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("logged-out session remained valid: %v", err)
	}
}

func TestSetupHTTPStateSeparatesAdministratorFromOnboarding(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := NewServer(store, "", false).WithSetupAgentConnectURL("https://center.example.com")
	statusResponse := httptest.NewRecorder()
	server.handleSetupStatus(statusResponse, httptest.NewRequest(http.MethodGet, "/api/v1/setup/status", nil))
	var status map[string]any
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["administratorConfigured"] != false || status["onboardingComplete"] != false || status["suggestedAgentConnectUrl"] != "https://center.example.com" {
		t.Fatalf("unexpected fresh setup status: %#v", status)
	}
	session, _, err := store.CreateFirstAdmin(context.Background(), "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	storeCloudflareOAuthIntegration(t, store, cloudflareOAuthToken{AccessToken: "access-secret", RefreshToken: "refresh-secret", ExpiresAt: time.Now().Add(time.Hour)})
	payload, _ := json.Marshal(InitialSetupInput{
		Site:    SiteInput{Name: "Home", Code: "home", Timezone: "Asia/Singapore"},
		Network: CenterNetworkInput{AgentConnectionMode: "lan", AgentConnectURL: "https://center.example.com"},
	})
	completeResponse := httptest.NewRecorder()
	completeRequest := httptest.NewRequest(http.MethodPost, "/api/v1/setup/complete", bytes.NewReader(payload))
	completeRequest.Header.Set("Content-Type", "application/json")
	server.handleSetupComplete(completeResponse, completeRequest)
	if completeResponse.Code != http.StatusCreated {
		t.Fatalf("complete setup status = %d, body = %q", completeResponse.Code, completeResponse.Body.String())
	}
	statusResponse = httptest.NewRecorder()
	server.handleSetupStatus(statusResponse, httptest.NewRequest(http.MethodGet, "/api/v1/setup/status", nil))
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["administratorConfigured"] != true || status["onboardingComplete"] != true {
		t.Fatalf("unexpected completed setup status: %#v", status)
	}
	if status["cloudflareConfigured"] != false || status["cloudflareZone"] != "" {
		t.Fatalf("unauthenticated setup status exposed Cloudflare configuration: %#v", status)
	}
	authorizedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/setup/status", nil)
	authorizedRequest.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
	statusResponse = httptest.NewRecorder()
	server.handleSetupStatus(statusResponse, authorizedRequest)
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["cloudflareConfigured"] != true || status["cloudflareZone"] != "example.com" {
		t.Fatalf("authenticated setup status hid Cloudflare configuration: %#v", status)
	}
}

func TestSetupStatusSuggestsTheKernelRouteForACloudPublicAddress(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	session, _, err := store.CreateFirstAdmin(context.Background(), "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	store.discoverNetworkCandidates = func(now time.Time) ([]networking.Candidate, error) {
		return []networking.Candidate{
			{Address: "10.0.0.157", Interface: "enp0s6", Kind: networking.KindLAN, ObservedAt: now},
			{Address: "10.77.0.6", Interface: "wg0", Kind: networking.KindLAN, ObservedAt: now},
		}, nil
	}
	store.lookupPublicAddress = func(context.Context) (string, error) { return "192.9.143.79", nil }
	store.lookupGatewayAddress = func(string) (string, error) { return "10.0.0.157", nil }
	server := NewServer(store, "", false).WithInfrastructureManager(&fakeBuiltinHeadscaleInstaller{})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/setup/status", nil)
	request.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
	response := httptest.NewRecorder()
	server.handleSetupStatus(response, request)
	var status map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["observedPublicAddress"] != "192.9.143.79" || status["suggestedGatewayAddress"] != "10.0.0.157" || status["publicAddressDetection"] != "cloud_mapping_candidate" {
		t.Fatalf("unexpected cloud public suggestion: %#v", status)
	}
}

func TestStatusIsLightweightAndDashboardRouteIsRemoved(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, _, err := store.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteInitialSetup(ctx, InitialSetupInput{
		Site:    SiteInput{Name: "Home", Code: "home", Timezone: "Asia/Singapore"},
		Network: CenterNetworkInput{AgentConnectionMode: "lan", AgentConnectURL: "https://center.example.com"},
	}); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	NewServer(store, "", false).handleStatus(response, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	var status map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{"catalogSources", "catalogApps", "agents", "deployments"} {
		if _, exists := status[removed]; exists {
			t.Fatalf("lightweight status still includes resource count %q: %#v", removed, status)
		}
	}
	if status["version"] != Version || status["agentConnectionMode"] != "lan" || status["agentConnectUrl"] != "https://center.example.com" {
		t.Fatalf("unexpected lightweight status: %#v", status)
	}

	response = httptest.NewRecorder()
	NewServer(store, "", false).Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("removed dashboard route status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestAdministratorCanChangePasswordAndRevokeOtherSessions(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	firstSession, firstCSRF, err := store.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	secondSession, _, err := store.Authenticate(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ChangePassword(ctx, firstSession, "wrong-current-password", "new-correct-horse-battery-staple"); err == nil {
		t.Fatal("wrong current password was accepted")
	}
	if err := store.ChangePassword(ctx, firstSession, "correct-horse-battery-staple", "new-correct-horse-battery-staple"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Authenticate(ctx, "admin", "correct-horse-battery-staple"); err == nil {
		t.Fatal("old password remained valid")
	}
	if _, _, err := store.Authenticate(ctx, "admin", "new-correct-horse-battery-staple"); err != nil {
		t.Fatalf("new password is not valid: %v", err)
	}
	if err := store.ValidateSession(ctx, firstSession, firstCSRF, true); err != nil {
		t.Fatalf("current session was revoked: %v", err)
	}
	if err := store.ValidateSession(ctx, secondSession, "", false); err == nil {
		t.Fatal("other administrator session was not revoked")
	}
}

func TestEncryptedBackupRestoresToNewDirectory(t *testing.T) {
	sourceDir := t.TempDir()
	store, err := Open(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSource(context.Background(), SourceInput{
		ID: "official", DisplayName: "Official", URL: "https://catalog.example.invalid/v1.json",
		PublicKey: make([]byte, 32), RefreshSeconds: 3600,
	}); err != nil {
		t.Fatal(err)
	}
	expectedCertificate := storeSystemCenterCertificateForTest(t, store, "center.example.invalid")
	expectedACMEKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	expectedACMEDER, err := x509.MarshalPKCS8PrivateKey(expectedACMEKey)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	acmeSecretID, err := store.putSecret(context.Background(), tx, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: expectedACMEDER}), "acme-account:"+letsencryptAccountID)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	acmeAccountURI := "https://acme-v02.api.letsencrypt.org/acme/acct/123456"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.Exec(`INSERT INTO certificate_authorities(id, account_uri, secret_id, created_at, updated_at) VALUES(?, ?, ?, ?, ?)`, letsencryptAccountID, acmeAccountURI, acmeSecretID, now, now); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	expectedACMEPublic, err := x509.MarshalPKIXPublicKey(&expectedACMEKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sourceSchema, err := sqliteSchemaVersion(context.Background(), store.db)
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "center.vastora")
	if err := store.Backup(context.Background(), backupPath, "backup-password-for-test"); err != nil {
		t.Fatal(err)
	}
	backupInfo, err := os.Stat(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if backupInfo.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %v, want 0600", backupInfo.Mode().Perm())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Restore(backupPath, filepath.Join(t.TempDir(), "restored"), "wrong-password"); err == nil {
		t.Fatal("restore accepted a wrong password")
	}
	tampered, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered[len(tampered)-1] ^= 0xff
	tamperedPath := filepath.Join(t.TempDir(), "tampered.vastora")
	if err := os.WriteFile(tamperedPath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(tamperedPath, filepath.Join(t.TempDir(), "tampered-restore"), "backup-password-for-test"); err == nil {
		t.Fatal("restore accepted a modified archive")
	}
	nonemptyDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(nonemptyDir, "keep"), []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(backupPath, nonemptyDir, "backup-password-for-test"); err == nil {
		t.Fatal("restore accepted a non-empty destination")
	}
	restoredDir := filepath.Join(t.TempDir(), "restored")
	if err := os.Mkdir(restoredDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Restore(backupPath, restoredDir, "backup-password-for-test"); err != nil {
		t.Fatal(err)
	}
	restoredDirectoryInfo, err := os.Stat(restoredDir)
	if err != nil {
		t.Fatal(err)
	}
	if restoredDirectoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("restored directory mode = %v, want 0700", restoredDirectoryInfo.Mode().Perm())
	}
	for _, name := range []string{"center.db", "center.key"} {
		info, err := os.Stat(filepath.Join(restoredDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("restored %s mode = %v, want 0600", name, info.Mode().Perm())
		}
	}
	restored, err := Open(restoredDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	sources, err := restored.ListSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].ID != "official" {
		t.Fatalf("unexpected restored sources: %#v", sources)
	}
	restoredSchema, err := sqliteSchemaVersion(context.Background(), restored.db)
	if err != nil {
		t.Fatal(err)
	}
	if restoredSchema != sourceSchema {
		t.Fatalf("restored schema = %d, want %d", restoredSchema, sourceSchema)
	}
	restoredCertificate, err := restored.loadSystemCenterCertificate(context.Background(), restored.db, "", "center.example.invalid")
	if err != nil {
		t.Fatalf("restored Center certificate is not decryptable: %v", err)
	}
	if restoredCertificate.CertificatePEM != expectedCertificate.CertificatePEM || restoredCertificate.PrivateKeyPEM != expectedCertificate.PrivateKeyPEM {
		t.Fatal("restored Center certificate does not match the encrypted source certificate")
	}
	restoredACME, err := restored.acmeClient(context.Background(), "")
	if err != nil {
		t.Fatalf("restored ACME account is not decryptable: %v", err)
	}
	restoredACMEPublic, err := x509.MarshalPKIXPublicKey(restoredACME.Key.Public())
	if err != nil {
		t.Fatal(err)
	}
	if string(restoredACME.KID) != acmeAccountURI || !bytes.Equal(restoredACMEPublic, expectedACMEPublic) {
		t.Fatal("restored ACME account does not match the encrypted source account")
	}
}

func TestEncryptedBackupRestoresACenterDatabaseLargerThan32MiB(t *testing.T) {
	sourceDir := t.TempDir()
	store, err := Open(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	largeValue := strings.Repeat("x", 33<<20)
	if _, err := store.db.Exec(`INSERT INTO settings(key, value) VALUES('large-backup-fixture', ?)`, largeValue); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "large-center.vastora")
	if err := store.Backup(context.Background(), backupPath, "large-backup-password"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restoredDir := filepath.Join(t.TempDir(), "restored")
	if err := Restore(backupPath, restoredDir, "large-backup-password"); err != nil {
		t.Fatalf("restore rejected a valid Center database larger than 32 MiB: %v", err)
	}
	restored, err := Open(restoredDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var restoredLength int
	if err := restored.db.QueryRow(`SELECT length(value) FROM settings WHERE key = 'large-backup-fixture'`).Scan(&restoredLength); err != nil {
		t.Fatal(err)
	}
	if restoredLength != len(largeValue) {
		t.Fatalf("restored large value length = %d, want %d", restoredLength, len(largeValue))
	}
}

func TestWebBackupDownloadIsEncryptedAndRestorable(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := NewServer(store, "", false)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/backups", strings.NewReader(`{"password":"web-backup-password"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.handleCreateBackup(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("backup status = %d, body = %q", response.Code, response.Body.String())
	}
	if !strings.HasPrefix(response.Body.String(), backupMagic) {
		t.Fatal("download is not a Vastora encrypted backup")
	}
	if !strings.Contains(response.Header().Get("Content-Disposition"), ".vastora") {
		t.Fatalf("missing backup filename: %q", response.Header().Get("Content-Disposition"))
	}
	backupPath := filepath.Join(t.TempDir(), "download.vastora")
	if err := os.WriteFile(backupPath, response.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(backupPath, filepath.Join(t.TempDir(), "restored"), "web-backup-password"); err != nil {
		t.Fatalf("downloaded backup is not restorable: %v", err)
	}
}

func TestDiagnosticsSummarizeHealthWithoutSecrets(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateSource(context.Background(), SourceInput{
		ID: "private", DisplayName: "Private", URL: "https://catalog.example.invalid/v1.json",
		PublicKey: make([]byte, 32), BearerToken: "diagnostic-must-not-leak", RefreshSeconds: 3600,
	}); err != nil {
		t.Fatal(err)
	}
	enrollment, err := store.CreateAgentEnrollment(context.Background(), AgentEnrollmentSpec{SiteID: testSiteID(t, store), Name: "retired", CenterURL: "https://center.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.EnrollAgent(context.Background(), enrollment.Token, "test", "linux", "amd64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DisableAgent(context.Background(), node.ID); err != nil {
		t.Fatal(err)
	}
	value, err := store.Diagnostics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if value.Nodes.Total != 1 || value.Nodes.Disabled != 1 || value.Schema != centerSchemaVersion {
		t.Fatalf("unexpected diagnostics: %#v", value)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("diagnostic-must-not-leak")) {
		t.Fatal("diagnostics leaked a secret")
	}
}

func TestCatalogSourceListRedactsBearerToken(t *testing.T) {
	t.Parallel()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateSource(context.Background(), SourceInput{
		ID: "private-catalog", DisplayName: "Private catalog", URL: "https://catalog.example.invalid/v1.json",
		PublicKey: make([]byte, 32), BearerToken: "never-return-this-value",
	}); err != nil {
		t.Fatal(err)
	}
	session, _, err := store.CreateFirstAdmin(context.Background(), "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/sources", nil)
	request.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
	response := httptest.NewRecorder()
	NewServer(store, "", false).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("catalog source API status = %d body=%s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("never-return-this-value")) {
		t.Fatal("bearer token leaked from the authenticated API")
	}
	var payload struct {
		Sources []CatalogSource `json:"sources"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Sources) != 1 || !payload.Sources[0].BearerTokenSet {
		t.Fatal("expected token metadata")
	}
}

func TestCatalogSourceRejectsCredentialsEmbeddedInURL(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	err = store.CreateSource(context.Background(), SourceInput{
		ID: "unsafe-catalog", DisplayName: "Unsafe catalog",
		URL: "https://catalog-user:catalog-password@example.invalid/v1.json", PublicKey: make([]byte, 32),
	})
	if err == nil || !strings.Contains(err.Error(), "without credentials") {
		t.Fatalf("catalog source URL credentials were accepted: %v", err)
	}
	sources, err := store.ListSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 0 {
		t.Fatalf("rejected catalog source was persisted: %#v", sources)
	}
	if _, err := store.db.Exec(`INSERT INTO catalog_sources(id, display_name, url, public_key, enabled, refresh_seconds, created_at)
		VALUES('legacy-unsafe-catalog', 'Legacy unsafe catalog', 'https://legacy-user:legacy-password@example.invalid/v1.json', ?, 1, 3600, ?)`, make([]byte, 32), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	sources, err = store.ListSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(sources)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("legacy-user")) || bytes.Contains(encoded, []byte("legacy-password")) {
		t.Fatalf("stored catalog URL credentials leaked through metadata: %s", encoded)
	}
	if len(sources) != 1 || sources[0].URL != "https://example.invalid/v1.json" {
		t.Fatalf("unexpected redacted catalog source metadata: %#v", sources)
	}
}

func TestOfficialCatalogMetadataKeepsItsBuiltinURL(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	payload, err := os.ReadFile("../../catalog/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SeedOfficialCatalog(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	sources, err := store.ListSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].ID != OfficialCatalogSourceID || sources[0].URL != "builtin://vastora-official" {
		t.Fatalf("unexpected official Catalog metadata: %#v", sources)
	}
}

func TestRegistryAndIntegrationResponsesExposeOnlySecretMetadata(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	credential, err := store.CreateRegistryCredential(ctx, "registry.example.invalid", "registry-user", "registry-secret-token")
	if err != nil {
		t.Fatal(err)
	}
	storeCloudflareOAuthIntegration(t, store, cloudflareOAuthToken{
		AccessToken: "cloudflare-access-secret", RefreshToken: "cloudflare-refresh-secret",
		ExpiresAt: time.Now().Add(time.Hour),
	})
	integration, err := store.Integration(ctx, "cloudflare")
	if err != nil {
		t.Fatal(err)
	}
	if !credential.TokenSet || !integration.SecretSet {
		t.Fatalf("secret metadata is missing: credential=%#v integration=%#v", credential, integration)
	}
	encoded, err := json.Marshal(map[string]any{"credential": credential, "integration": integration})
	if err != nil {
		t.Fatal(err)
	}
	for _, secretValue := range []string{"registry-secret-token", "cloudflare-access-secret", "cloudflare-refresh-secret"} {
		if bytes.Contains(encoded, []byte(secretValue)) {
			t.Fatalf("secret response leaked %q: %s", secretValue, encoded)
		}
	}
	var registrySealed []byte
	if err := store.db.QueryRow(`SELECT sealed FROM secrets WHERE id = (SELECT secret_id FROM registry_credentials WHERE id = ?)`, credential.ID).Scan(&registrySealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(registrySealed, []byte("registry-secret-token")) {
		t.Fatal("registry token was stored in plaintext")
	}
}

func TestEmptyCatalogListsAreJSONArrays(t *testing.T) {
	t.Parallel()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sources, err := store.ListSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	apps, err := store.ListApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	for name, value := range map[string]any{"sources": sources, "apps": apps} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != "[]" {
			t.Fatalf("empty %s encoded as %s, want []", name, encoded)
		}
	}
}
