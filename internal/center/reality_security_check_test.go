package center

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestRealitySecurityCheckStoresSafeFiniteResult(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	seedRealitySecurityCheckPublication(t, store)

	var callsMu sync.Mutex
	calls := map[string]int{}
	store.dialRealitySecurityProbe = func(_ context.Context, address, serverName string) error {
		if address != "203.0.113.40" {
			return errors.New("unexpected address")
		}
		callsMu.Lock()
		calls[serverName]++
		callsMu.Unlock()
		if serverName == "www.intel.com" {
			return nil
		}
		return errors.New("rejected")
	}

	result, err := store.RunRealitySecurityCheck(context.Background(), "verification-publication", "security-admin")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != realitySecurityCheckSafe || result.Scope != realitySecurityCheckRemote || len(result.Checks) != 5 {
		t.Fatalf("unexpected security result: %#v", result)
	}
	if calls["www.intel.com"] != 1 || calls["api.openai.com"] != 1 || calls["www.cloudflare.com"] != 1 || calls[""] != 1 || len(calls) != 5 {
		t.Fatalf("unexpected probe set: %#v", calls)
	}
	publication, err := store.Publication(context.Background(), "verification-publication")
	if err != nil {
		t.Fatal(err)
	}
	if publication.SecurityCheck == nil || publication.SecurityCheck.Status != realitySecurityCheckSafe {
		t.Fatalf("publication did not expose the stored check: %#v", publication.SecurityCheck)
	}
	var auditMessage string
	if err := store.db.QueryRow(`SELECT message FROM task_events WHERE kind = 'security.reality.check' ORDER BY created_at DESC LIMIT 1`).Scan(&auditMessage); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(auditMessage, "safe (remote)") || !strings.Contains(auditMessage, "security-admin") {
		t.Fatalf("unexpected security audit: %q", auditMessage)
	}
	if _, err := store.db.Exec(`UPDATE three_x_ui_reality_guards SET status = 'action_required' WHERE service_id = 'verification-service'`); err != nil {
		t.Fatal(err)
	}
	publication, err = store.Publication(context.Background(), "verification-publication")
	if err != nil {
		t.Fatal(err)
	}
	if publication.SecurityCheck != nil {
		t.Fatalf("unsafe guard still exposed a prior security result: %#v", publication.SecurityCheck)
	}
}

func TestRealitySecurityCheckReportsAffectedAndInconclusive(t *testing.T) {
	tests := []struct {
		name       string
		probe      func(string) error
		wantStatus string
	}{
		{
			name: "unauthorized target reached",
			probe: func(serverName string) error {
				if serverName == "www.intel.com" || serverName == "api.openai.com" {
					return nil
				}
				return errors.New("rejected")
			},
			wantStatus: realitySecurityCheckAffected,
		},
		{
			name: "expected fallback unavailable",
			probe: func(string) error {
				return errors.New("rejected")
			},
			wantStatus: realitySecurityCheckInconclusive,
		},
		{
			name: "negative probe timeout",
			probe: func(serverName string) error {
				if serverName == "www.intel.com" {
					return nil
				}
				if serverName == "www.cloudflare.com" {
					return context.DeadlineExceeded
				}
				return errors.New("rejected")
			},
			wantStatus: realitySecurityCheckInconclusive,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			seedRealitySecurityCheckPublication(t, store)
			store.dialRealitySecurityProbe = func(_ context.Context, _, serverName string) error {
				return test.probe(serverName)
			}
			result, err := store.RunRealitySecurityCheck(context.Background(), "verification-publication", "security-admin")
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != test.wantStatus {
				t.Fatalf("status=%q checks=%#v, want %q", result.Status, result.Checks, test.wantStatus)
			}
		})
	}
}

func TestRealitySecurityCheckRejectsChangedOrUnsupportedPublication(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	seedRealitySecurityCheckPublication(t, store)
	ctx := context.Background()

	target, err := store.realitySecurityCheckTarget(ctx, "verification-publication")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE publications SET desired_revision = desired_revision + 1 WHERE id = 'verification-publication'`); err != nil {
		t.Fatal(err)
	}
	result := RealitySecurityCheckView{
		Status: realitySecurityCheckSafe,
		Scope:  realitySecurityCheckRemote,
		Checks: []RealitySecurityCheckItem{
			{Kind: "expected_fallback", Status: "passed", Reason: "expected_fallback_verified"},
			{Kind: "openai_sni", Status: "passed", Reason: "unauthorized_destination_rejected"},
			{Kind: "cloudflare_sni", Status: "passed", Reason: "unauthorized_destination_rejected"},
			{Kind: "random_sni", Status: "passed", Reason: "unauthorized_destination_rejected"},
			{Kind: "no_sni", Status: "passed", Reason: "unauthorized_destination_rejected"},
		},
		CheckedAt: time.Now().UTC(),
	}
	if err := store.storeRealitySecurityCheck(ctx, target, "security-admin", result); err == nil || !strings.Contains(err.Error(), "changed during") {
		t.Fatalf("stale security result was accepted: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE publications SET desired_revision = 1, kind = 'public_direct' WHERE id = 'verification-publication'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RunRealitySecurityCheck(ctx, "verification-publication", "security-admin"); err == nil || !strings.Contains(err.Error(), "node-direct shared 443") {
		t.Fatalf("unsupported publication was accepted: %v", err)
	}
}

func TestRealitySecurityCheckMarksSameHostScope(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	seedRealitySecurityCheckPublication(t, store)
	store.discoverNetworkCandidates = func(time.Time) ([]networking.Candidate, error) {
		return []networking.Candidate{{Address: "203.0.113.40", Interface: "eth0", Kind: networking.KindPublic}}, nil
	}
	store.dialRealitySecurityProbe = func(_ context.Context, _, serverName string) error {
		if serverName == "www.intel.com" {
			return nil
		}
		return errors.New("rejected")
	}
	result, err := store.RunRealitySecurityCheck(context.Background(), "verification-publication", "security-admin")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != realitySecurityCheckSafe || result.Scope != realitySecurityCheckSameHost {
		t.Fatalf("unexpected same-host result: %#v", result)
	}
}

func TestRealitySecurityCheckEndpointRequiresAdminAndReturnsNoStoreResult(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	session, csrf, err := store.CreateFirstAdmin(context.Background(), "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	seedRealitySecurityCheckPublication(t, store)
	store.dialRealitySecurityProbe = func(_ context.Context, _, serverName string) error {
		if serverName == "www.intel.com" {
			return nil
		}
		return errors.New("rejected")
	}
	handler := NewServer(store, "", false).Handler()

	unauthorized := httptest.NewRequest(http.MethodPost, "/api/v1/publications/verification-publication/security-check", nil)
	unauthorizedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%q", unauthorizedResponse.Code, unauthorizedResponse.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/publications/verification-publication/security-check", nil)
	request.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
	request.AddCookie(&http.Cookie{Name: "vastora_csrf", Value: csrf})
	request.Header.Set("X-CSRF-Token", csrf)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("security check response status=%d cache=%q body=%q", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	var result RealitySecurityCheckView
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Status != realitySecurityCheckSafe || len(result.Checks) != 5 {
		t.Fatalf("security check response=%#v err=%v", result, err)
	}
}

func seedRealitySecurityCheckPublication(t *testing.T, store *Store) {
	t.Helper()
	seedVerificationPublication(t, store, publicationShared443, "manual", 1, 1, "ready")
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	statements := []struct {
		query string
		args  []any
	}{
		{`UPDATE applications SET app_key = ?, role = 'worker' WHERE id = 'verification-app'`, []any{threeXUIAppKey}},
		{`UPDATE services SET app_protocol = 'vless/tcp/reality', status = 'ready' WHERE id = 'verification-service'`, nil},
		{`UPDATE publications SET sni_hostname = 'www.intel.com', status = 'ready' WHERE id = 'verification-publication'`, nil},
		{`INSERT INTO three_x_ui_reality_guards(service_id, target_host, target_ip, server_name, companion_tag, revision, status, verified_at, created_at, updated_at)
			VALUES('verification-service', 'www.intel.com', '192.0.2.80', 'www.intel.com', '', 1, 'ready', ?, ?, ?)`, []any{now, now, now}},
		{`INSERT INTO admins(id, username, password_hash, created_at) VALUES('security-admin', 'security-admin', 'test-hash', ?)`, []any{now}},
	}
	for _, statement := range statements {
		if _, err := store.db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	store.discoverNetworkCandidates = func(time.Time) ([]networking.Candidate, error) {
		return []networking.Candidate{{Address: "198.51.100.8", Interface: "eth0", Kind: networking.KindPublic}}, nil
	}
}
