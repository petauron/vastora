package center

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDueSystemEndpointAliasIsQuarantinedBeforeCertificateExpiry(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	storeSystemCenterCertificateForTest(t, store, "center.example.com")
	var secretID string
	if err := store.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, systemCenterCertificateSecretSetting).Scan(&secretID); err != nil {
		t.Fatal(err)
	}
	insertSystemEndpointTransitionForTest(t, store, secretID, now.Add(6*24*time.Hour), now.Add(30*24*time.Hour))
	changed, err := store.beginDueSystemEndpointAliasRetirements(ctx)
	if err != nil || !changed {
		t.Fatalf("due transition was not quarantined: changed=%v err=%v", changed, err)
	}
	active, err := readActiveSystemEndpointAliases(ctx, store.db, "center")
	if err != nil || len(active) != 0 {
		t.Fatalf("retiring Center alias remained active: aliases=%#v err=%v", active, err)
	}
	aliases, err := store.ListSystemEndpointAliases(ctx)
	if err != nil || len(aliases) != 2 || aliases[0].LifecycleState != "retiring" || aliases[1].LifecycleState != "retiring" {
		t.Fatalf("unexpected lifecycle state: aliases=%#v err=%v", aliases, err)
	}
}

func TestSystemEndpointAliasRetirementDeletesOnlyOwnedDNSAfterRuntimeRemoval(t *testing.T) {
	deleted := false
	cloudflare := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/zones":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":[{"id":"zone","name":"example.com","account":{"id":"account","name":"Account"}}],"result_info":{"total_pages":1}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/accounts/account/cfd_tunnel":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
		case request.Method == http.MethodGet && request.URL.Path == "/zones/zone/dns_records" && request.URL.Query().Get("name") == "headscale.old.example.com":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":[{"id":"old-record","type":"A","name":"headscale.old.example.com","content":"203.0.113.10","proxied":false}]}`))
		case request.Method == http.MethodDelete && request.URL.Path == "/zones/zone/dns_records/old-record":
			deleted = true
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
	ctx := context.Background()
	store.cloudflareOAuth = cloudflareOAuthConfig{APIURL: cloudflare.URL, HTTPClient: cloudflare.Client()}
	storeCloudflareOAuthIntegration(t, store, cloudflareOAuthToken{AccessToken: "access-token", RefreshToken: "refresh-token", ExpiresAt: time.Now().Add(time.Hour)})
	storeSystemCenterCertificateForTest(t, store, "center.example.com")
	var secretID string
	if err := store.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, systemCenterCertificateSecretSetting).Scan(&secretID); err != nil {
		t.Fatal(err)
	}
	now := store.now().UTC()
	insertSystemEndpointTransitionForTest(t, store, secretID, now.Add(60*24*time.Hour), now.Add(30*24*time.Hour))
	if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?), (?, ?), (?, ?)`,
		agentConnectionModeSetting, "headscale", agentConnectURLSetting, "https://center.example.com", builtinHeadscaleRuntimeSetting, builtinHeadscaleRuntimeVersion); err != nil {
		t.Fatal(err)
	}
	stamp := now.Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO network_integrations(kind, mode, endpoint, status, created_at, updated_at)
		VALUES('headscale', 'builtin', 'https://headscale.example.com', 'configured', ?, ?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	installer := &fakeBuiltinHeadscaleInstaller{}
	server := NewServer(store, "", false).WithInfrastructureManager(installer)
	if err := server.RetireSystemEndpointAliases(ctx, "transition"); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("owned old Headscale DNS record was not deleted")
	}
	if len(installer.reconcileInput.CenterAliases) != 0 || len(installer.reconcileInput.HeadscaleAliases) != 0 {
		t.Fatalf("retiring aliases remained in the fixed runtime: %#v", installer.reconcileInput)
	}
	var aliases int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM system_endpoint_aliases`).Scan(&aliases); err != nil || aliases != 0 {
		t.Fatalf("retired aliases remain: count=%d err=%v", aliases, err)
	}
}

func insertSystemEndpointTransitionForTest(t *testing.T, store *Store, secretID string, certificateNotAfter, retireAfter time.Time) {
	t.Helper()
	now := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO system_endpoint_aliases(kind, endpoint, certificate_secret_id, certificate_not_after,
		transition_id, lifecycle_state, retire_after, created_at, updated_at)
		VALUES('center', 'https://center.old.example.com', ?, ?, 'transition', 'active', ?, ?, ?)`, secretID, certificateNotAfter.Format(time.RFC3339Nano), retireAfter.Format(time.RFC3339Nano), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO system_endpoint_aliases(kind, endpoint, transition_id, lifecycle_state, retire_after,
		dns_account_id, dns_zone_id, dns_record_id, dns_record_type, dns_record_content, created_at, updated_at)
		VALUES('headscale', 'https://headscale.old.example.com', 'transition', 'active', ?, 'account', 'zone', 'old-record', 'A', '203.0.113.10', ?, ?)`, retireAfter.Format(time.RFC3339Nano), now, now); err != nil {
		t.Fatal(err)
	}
}
