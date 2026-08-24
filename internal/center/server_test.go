package center

import (
	"bytes"
	"context"
	"encoding/json"
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
	}{
		{name: "existing asset", target: "http://example.test/asset.txt", wantBody: "public-asset"},
		{name: "SPA fallback", target: "http://example.test/dashboard/settings", wantBody: "spa-index"},
		{name: "path traversal", target: "http://example.test/%2e%2e/secret.txt", forbidBody: "outside-secret"},
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
		})
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
			{Address: "10.0.0.157", Interface: "enp0s6", Family: "ipv4", Kind: networking.KindLAN, ObservedAt: now},
			{Address: "10.77.0.6", Interface: "wg0", Family: "ipv4", Kind: networking.KindLAN, ObservedAt: now},
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
	backupPath := filepath.Join(t.TempDir(), "center.vastora")
	if err := store.Backup(context.Background(), backupPath, "backup-password-for-test"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Restore(backupPath, filepath.Join(t.TempDir(), "restored"), "wrong-password"); err == nil {
		t.Fatal("restore accepted a wrong password")
	}
	restoredDir := filepath.Join(t.TempDir(), "restored")
	if err := Restore(backupPath, restoredDir, "backup-password-for-test"); err != nil {
		t.Fatal(err)
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
	node, err := store.EnrollAgent(context.Background(), enrollment.Token, "test", "linux", "amd64")
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
	sources, err := store.ListSources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(sources)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("never-return-this-value")) {
		t.Fatal("bearer token leaked from metadata")
	}
	if !sources[0].BearerTokenSet {
		t.Fatal("expected token metadata")
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
