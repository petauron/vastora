package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAccessSessionDefaultsValidationAndPersistence(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	view, err := store.CenterRemoteAccess(ctx, false)
	if err != nil || view.AccessSessionDuration != "24h" || view.AccessSessionSync.Status != "not_synced" {
		t.Fatalf("default view: %+v, %v", view, err)
	}
	for _, value := range []string{"15m", "30m", "1h", "6h", "12h", "24h", "48h", "72h", "168h", "720h"} {
		if !validAccessSessionDuration(value) {
			t.Errorf("rejected %s", value)
		}
	}
	for _, value := range []string{"", "0", "0s", "-1h", "24", "forever", "731h", "9999999999999999999999h", "1h<script>"} {
		if validAccessSessionDuration(value) {
			t.Errorf("accepted %q", value)
		}
		if err := store.saveAccessSessionSettings(ctx, value, AccessSessionSync{}); err == nil {
			t.Errorf("stored invalid %q", value)
		}
	}
	server := NewServer(store, "", false)
	for _, input := range []CenterRemoteAccessInput{
		{Enabled: true, AccessSessionDuration: "-1h"},
		{Enabled: true, ProtectionMode: " native ", AccessSessionDuration: "6h"},
		{Enabled: false, AccessSessionDuration: "6h"},
	} {
		if _, err := server.ConfigureCenterRemoteAccess(ctx, input, ""); err == nil || !strings.Contains(err.Error(), "session duration") {
			t.Fatalf("invalid input reached remote configuration: %v", err)
		}
	}
	want := AccessSessionSync{Status: "partial", Total: 2, Updated: 1, FailedHosts: []string{"panel.example.com"}}
	if err := store.saveAccessSessionSettings(ctx, "6h", want); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	view, err = store.CenterRemoteAccess(ctx, true)
	if err != nil || view.AccessSessionDuration != "6h" || !reflect.DeepEqual(view.AccessSessionSync, want) {
		t.Fatalf("reopened settings: %+v, %v", view, err)
	}
}

type accessSessionAPI struct {
	mu           sync.Mutex
	t            *testing.T
	durations    map[string]string
	puts         map[string]int
	failService  bool
	postDuration string
}

func (api *accessSessionAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	api.mu.Lock()
	defer api.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	respond := func(result any) { _ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result}) }
	if r.Method == "GET" && r.URL.Path == "/zones/zone" {
		respond(map[string]string{"name": "example.com"})
		return
	}
	if r.Method == "POST" && r.URL.Path == "/accounts/account/access/apps" {
		var app map[string]any
		if err := json.NewDecoder(r.Body).Decode(&app); err != nil {
			api.t.Error(err)
		}
		api.postDuration, _ = app["session_duration"].(string)
		respond(map[string]string{"id": "new-service-app"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/accounts/account/access/apps/")
	id := strings.TrimSuffix(path, "/policies")
	domain := "center-vastora.example.com"
	if id == "service-app" {
		domain = "verification.example.test"
	}
	if _, ok := api.durations[id]; !ok {
		api.t.Errorf("touched unrelated endpoint: %s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected", 500)
		return
	}
	if strings.HasSuffix(path, "/policies") && r.Method == "GET" {
		policy := map[string]any{"id": "existing-policy", "include": []any{map[string]any{"email_domain": map[string]string{"domain": "example.org"}}}}
		if id == "service-app" {
			policy["session_duration"] = "1h"
		}
		respond([]any{policy})
		return
	}
	if r.Method == "GET" {
		respond(map[string]any{
			"id": id, "type": "self_hosted", "domain": domain, "name": "Original name", "aud": "unchanged-aud",
			"session_duration": api.durations[id], "allowed_idps": []string{"otp"}, "auto_redirect_to_identity": true,
			"app_launcher_visible": false, "http_only_cookie_attribute": true,
			"policies": []any{map[string]any{"id": "existing-policy", "account_id": "account", "precedence": 3}},
		})
		return
	}
	if r.Method == "PUT" {
		api.puts[id]++
		if id == "service-app" && api.failService {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"message":"temporary upstream failure"}]}`))
			return
		}
		var app map[string]any
		if err := json.NewDecoder(r.Body).Decode(&app); err != nil {
			api.t.Error(err)
		}
		if app["domain"] != domain || app["name"] != "Original name" || app["http_only_cookie_attribute"] != true || app["auto_redirect_to_identity"] != true || app["app_launcher_visible"] != false || !reflect.DeepEqual(app["allowed_idps"], []any{"otp"}) {
			api.t.Errorf("unrelated app settings changed: %+v", app)
		}
		if !reflect.DeepEqual(app["policies"], []any{map[string]any{"id": "existing-policy", "account_id": "account", "precedence": float64(3)}}) {
			api.t.Errorf("policy attachments changed: %+v", app["policies"])
		}
		if _, ok := app["aud"]; ok {
			api.t.Error("sent response-only aud")
		}
		api.durations[id], _ = app["session_duration"].(string)
		respond(map[string]string{"id": id, "session_duration": api.durations[id]})
		return
	}
	api.t.Errorf("destructive/unexpected request: %s %s", r.Method, r.URL.Path)
	http.Error(w, "unexpected", 500)
}

func seedAccessSessionCenter(t *testing.T, store *Store) {
	t.Helper()
	_, err := store.db.Exec(`INSERT INTO center_remote_access(id,hostname,audience_kind,audience_value,otp_identity_provider_id,access_application_id,protection_mode,status,created_at,updated_at)
		VALUES(1,'center-vastora.example.com','email','admin@example.com','otp','center-app','access','configured','2026-09-09T00:00:00Z','2026-09-09T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestAccessSessionSaveSynchronizesManagedEntriesAndRetriesInPlace(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedAccessSessionCenter(t, store)
	seedVerificationPublication(t, store, "public_direct", "cloudflare", 1, 1, "ready")
	if _, err := store.db.Exec(`UPDATE publications SET kind = 'cloudflare_tunnel', ingress_owner = 'tunnel_connector', access_application_id = 'service-app' WHERE id = 'verification-publication'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO publications(id, service_id, kind, ingress_owner, entry_node_id, hostname, dns_provider, access_application_id, desired_revision, applied_revision, status, created_at, updated_at)
		SELECT 'stopped-entry', service_id, kind, ingress_owner, entry_node_id, 'stopped.example.test', dns_provider, 'do-not-touch', 1, 1, 'stopped', created_at, updated_at FROM publications WHERE id = 'verification-publication'`); err != nil {
		t.Fatal(err)
	}
	fake := &accessSessionAPI{t: t, durations: map[string]string{"center-app": "24h", "service-app": "24h"}, puts: map[string]int{}, failService: true}
	remote := httptest.NewServer(fake)
	defer remote.Close()
	store.cloudflareOAuth = cloudflareOAuthConfig{APIURL: remote.URL, HTTPClient: remote.Client()}
	storeCloudflareOAuthIntegration(t, store, cloudflareOAuthToken{AccessToken: "access", RefreshToken: "refresh", Scope: "zone.read access.write access-acct.write", ExpiresAt: time.Now().Add(time.Hour)})
	server := NewServer(store, "", false).WithInfrastructureManager(&fakeBuiltinHeadscaleInstaller{})
	input := CenterRemoteAccessInput{Enabled: true, ProtectionMode: "access", AudienceKind: "email", AudienceValue: "admin@example.com", AccessSessionDuration: "6h"}
	view, err := server.ConfigureCenterRemoteAccess(context.Background(), input, "https://center.example.com")
	if err != nil || !view.Enabled || view.Status != "configured" || view.AccessSessionDuration != "6h" || view.AccessSessionSync.Status != "partial" || view.AccessSessionSync.Updated != 1 || view.AccessSessionSync.Total != 2 {
		t.Fatalf("partial sync lost working entry: %+v, %v", view, err)
	}
	if !reflect.DeepEqual(view.AccessSessionSync.FailedHosts, []string{"verification.example.test"}) || !reflect.DeepEqual(view.AccessSessionSync.PolicyOverrideHosts, []string{"verification.example.test"}) {
		t.Fatalf("missing failure/override: %+v", view)
	}
	fake.mu.Lock()
	fake.failService = false
	fake.mu.Unlock()
	// Omitted input preserves the saved duration rather than reverting to 24h.
	input.AccessSessionDuration = ""
	view, err = server.ConfigureCenterRemoteAccess(context.Background(), input, "https://center.example.com")
	if err != nil || view.AccessSessionSync.Status != "synced" || view.AccessSessionSync.Updated != 2 || view.AccessSessionDuration != "6h" {
		t.Fatalf("retry: %+v %v", view, err)
	}
	fake.mu.Lock()
	if fake.puts["center-app"] != 1 || fake.puts["service-app"] != 2 {
		t.Errorf("retry rewrote successful apps: %+v", fake.puts)
	}
	fake.mu.Unlock()
	record, _, err := store.centerRemoteAccessRecord(context.Background())
	if err != nil || record.ApplicationID != "center-app" || record.IdentityProviderID != "otp" {
		t.Fatalf("identity changed: %+v %v", record, err)
	}
	// New service entries inherit the global duration after this change.
	if _, err := store.db.Exec(`UPDATE publications SET access_application_id = '' WHERE id = 'verification-publication'`); err != nil {
		t.Fatal(err)
	}
	if err := store.ensureCloudflareServiceAccess(context.Background(), "verification-publication", 1, "verification.example.test"); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	if fake.postDuration != "6h" {
		t.Errorf("new entry duration: %q", fake.postDuration)
	}
	fake.mu.Unlock()
}

func TestAccessSessionFailureRetainsDesiredSettingAndProtection(t *testing.T) {
	for _, scenario := range []string{"timeout", "forbidden", "wrong-identity", "unconfirmed-update", "policy-read-failure"} {
		t.Run(scenario, func(t *testing.T) {
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			seedAccessSessionCenter(t, store)
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" && r.Method != "PUT" {
					t.Errorf("unexpected mutation: %s", r.Method)
				}
				w.Header().Set("Content-Type", "application/json")
				if scenario == "timeout" {
					<-r.Context().Done()
					return
				}
				if scenario == "forbidden" || scenario == "policy-read-failure" && strings.HasSuffix(r.URL.Path, "/policies") {
					w.WriteHeader(403)
					_, _ = w.Write([]byte(`{"success":false}`))
					return
				}
				if strings.HasSuffix(r.URL.Path, "/policies") {
					_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
					return
				}
				if scenario == "wrong-identity" {
					_, _ = w.Write([]byte(`{"success":true,"result":{"id":"unrelated-app"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"success":true,"result":{"id":"center-app","domain":"center-vastora.example.com","type":"self_hosted","session_duration":"24h","policies":[{"id":"policy","precedence":1}]}}`))
			}))
			defer remote.Close()
			client := cloudflareClient{accountID: "account", baseURL: remote.URL, http: &http.Client{Timeout: 50 * time.Millisecond}}
			if err := store.syncAccessSessions(context.Background(), client, "6h", ""); err != nil {
				t.Fatal(err)
			}
			view, err := store.CenterRemoteAccess(context.Background(), true)
			if err != nil || !view.Enabled || view.AccessSessionDuration != "6h" || view.AccessSessionSync.Status != "failed" || view.AccessSessionSync.Updated != 0 {
				t.Fatalf("failure result: %+v %v", view, err)
			}
		})
	}
}

func TestAccessSessionMigrationFrom65PreservesEntriesAndBacksUp(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	legacy := &Store{db: db}
	if err := legacy.initializeMigrationHistory(ctx, schemaBaselineVersion); err != nil {
		t.Fatal(err)
	}
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 65); err != nil {
		t.Fatal(err)
	}
	seedAccessSessionCenter(t, legacy)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	view, err := current.CenterRemoteAccess(ctx, true)
	if err != nil || view.AccessSessionDuration != "24h" || !view.Enabled || view.AudienceValue != "admin@example.com" {
		t.Fatalf("migration: %+v %v", view, err)
	}
	record, _, err := current.centerRemoteAccessRecord(ctx)
	if err != nil || record.ApplicationID != "center-app" || record.IdentityProviderID != "otp" {
		t.Fatalf("identity lost: %+v %v", record, err)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", "center-v65-before-v66-*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("backup missing: %v %v", backups, err)
	}
}

func TestAccessSessionCreationUsesConfiguredDuration(t *testing.T) {
	for _, duration := range []string{"24h", "15m", "720h"} {
		t.Run(duration, func(t *testing.T) {
			fake := &accessSessionAPI{t: t}
			remote := httptest.NewServer(fake)
			defer remote.Close()
			client := cloudflareClient{accountID: "account", baseURL: remote.URL, http: remote.Client()}
			id, err := client.createAccessApplication(context.Background(), "Vastora Center", "center-vastora.example.com", "email", "admin@example.com", "otp", duration)
			if err != nil || id == "" {
				t.Fatalf("create: %s %v", id, err)
			}
			fake.mu.Lock()
			if fake.postDuration != duration {
				t.Errorf("created duration: %s", fake.postDuration)
			}
			fake.mu.Unlock()
		})
	}
}

func TestAccessSessionCancellationPersistsRetryableResult(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedAccessSessionCenter(t, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
	}))
	defer remote.Close()
	client := cloudflareClient{accountID: "account", baseURL: remote.URL, http: remote.Client()}
	if err := store.syncAccessSessions(ctx, client, "12h", ""); err != nil {
		t.Fatal(err)
	}
	view, err := store.CenterRemoteAccess(context.Background(), true)
	if err != nil || view.AccessSessionDuration != "12h" || view.AccessSessionSync.Status != "failed" || !view.Enabled {
		t.Fatalf("lost canceled result: %+v %v", view, err)
	}
}
