package center

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestSwitchSystemDomainMovesPrimaryEndpointsAndKeepsAliases(t *testing.T) {
	ctx := context.Background()
	cloudflare := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/zones":
			if request.Header.Get("Authorization") != "Bearer saved-access-token" {
				t.Fatalf("domain switch did not reuse the saved Cloudflare token")
			}
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":[{"id":"zone","name":"old.example.com","account":{"id":"account","name":"Old account"}},{"id":"new-zone","name":"new.example.com","account":{"id":"new-account","name":"New account"}}],"result_info":{"total_pages":1}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/accounts/new-account/cfd_tunnel":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
		case request.Method == http.MethodGet && request.URL.Path == "/zones/new-zone":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{"name":"new.example.com"}}`))
		case request.Method == http.MethodGet && request.URL.Path == "/zones/new-zone/dns_records" && request.URL.Query().Get("name") == "headscale.vastora.new.example.com":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
		case request.Method == http.MethodPost && request.URL.Path == "/zones/new-zone/dns_records":
			_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{"id":"new-headscale-record"}}`))
		default:
			t.Fatalf("unexpected Cloudflare request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer cloudflare.Close()

	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.cloudflareOAuth = cloudflareOAuthConfig{APIURL: cloudflare.URL, HTTPClient: cloudflare.Client()}
	storeCloudflareOAuthIntegration(t, store, cloudflareOAuthToken{AccessToken: "saved-access-token", RefreshToken: "saved-refresh-token", ExpiresAt: time.Now().Add(time.Hour)})
	store.discoverNetworkCandidates = func(time.Time) ([]networking.Candidate, error) {
		return []networking.Candidate{{Address: "100.64.0.1", Interface: "tailscale0", Family: "ipv4", Kind: networking.KindHeadscale}}, nil
	}
	store.issueDomainCertificate = func(_ context.Context, _ cloudflareClient, zone string, names ...string) (managedCertificate, error) {
		if zone != "new.example.com" || len(names) != 1 || names[0] != "center.vastora.new.example.com" {
			t.Fatalf("unexpected certificate request: zone=%q names=%v", zone, names)
		}
		return testManagedCertificate(t, names...), nil
	}
	storeSystemCenterCertificateForTest(t, store, "center.vastora.old.example.com")
	oldNamespace := "vastora.old.example.com"
	site, err := store.CreateSite(ctx, SiteInput{Name: "Home", Code: "home", Timezone: "UTC", DomainSuffix: oldNamespace})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO settings(key, value) VALUES(?, ?), (?, ?), (?, ?)`, []any{agentConnectionModeSetting, "headscale", agentConnectURLSetting, "https://center." + oldNamespace, setupGatewayBindingSetting, `{"publicAddress":"203.0.113.10","bindAddress":"203.0.113.10"}`}},
		{`INSERT INTO network_integrations(kind, mode, endpoint, status, created_at, updated_at) VALUES('headscale', 'builtin', ?, 'configured', ?, ?)`, []any{"https://headscale." + oldNamespace, now, now}},
		{`INSERT INTO agents(id, name, credential_hash, version, status, enrolled_at, last_seen_at, site_id) VALUES('center-node', 'Center', X'01', 'test', 'active', ?, ?, ?)`, []any{now, now, site.ID}},
		{`INSERT INTO agent_network_candidates(agent_id, address, interface_name, family, kind, observed_at) VALUES('center-node', '100.64.0.1', 'tailscale0', 'ipv4', 'headscale', ?)`, []any{now}},
		{`INSERT INTO agent_network_profiles(agent_id, service_address, headscale_address, enabled_kinds_json, confirmed_at, candidate_observed_at) VALUES('center-node', '100.64.0.1', '100.64.0.1', '["headscale"]', ?, ?)`, []any{now, now}},
	}
	for _, statement := range statements {
		if _, err := store.db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	installer := &fakeBuiltinHeadscaleInstaller{}
	server := NewServer(store, "", false).WithInfrastructureManager(installer)
	var previousSecretID string
	if err := store.db.QueryRowContext(ctx, `SELECT secret_id FROM network_integrations WHERE kind = 'cloudflare'`).Scan(&previousSecretID); err != nil {
		t.Fatal(err)
	}
	result, err := server.SwitchSystemDomain(ctx, SystemDomainSwitchInput{ZoneID: "new-zone", Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.CenterURL != "https://center.vastora.new.example.com" || result.HeadscaleURL != "https://headscale.vastora.new.example.com" || result.Namespace != "vastora.new.example.com" || !result.BackupCreated {
		t.Fatalf("unexpected domain result: %#v", result)
	}
	if installer.reconcileInput.CenterURL != result.CenterURL || installer.reconcileInput.HeadscaleURL != result.HeadscaleURL || installer.reconcileInput.CenterPrivateBindAddress != "100.64.0.1" || len(installer.reconcileInput.CenterAliases) != 1 || installer.reconcileInput.CenterAliases[0].URL != "https://center."+oldNamespace || len(installer.reconcileInput.HeadscaleAliases) != 1 {
		t.Fatalf("unexpected deployment request: %#v", installer.reconcileInput)
	}
	updatedSite, err := store.Site(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedSite.DomainSuffix != result.Namespace {
		t.Fatalf("site namespace = %q", updatedSite.DomainSuffix)
	}
	aliases, err := store.ListSystemEndpointAliases(ctx)
	if err != nil || len(aliases) != 2 {
		t.Fatalf("aliases = %#v, err = %v", aliases, err)
	}
	var encoded string
	if err := store.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'cloudflare_setup_dns_records'`).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var records []SetupDNSRecord
	if json.Unmarshal([]byte(encoded), &records) != nil || len(records) != 1 || records[0].ID != "new-headscale-record" {
		t.Fatalf("tracked DNS records = %s", encoded)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "domain-switch-backups", "*.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("domain backups = %v, err = %v", backups, err)
	}
	var endpoint, accountID, zoneID, currentSecretID string
	if err := store.db.QueryRowContext(ctx, `SELECT endpoint, account_id, zone_id, secret_id FROM network_integrations WHERE kind = 'cloudflare'`).Scan(&endpoint, &accountID, &zoneID, &currentSecretID); err != nil {
		t.Fatal(err)
	}
	if endpoint != "new.example.com" || accountID != "new-account" || zoneID != "new-zone" || currentSecretID != previousSecretID {
		t.Fatalf("Cloudflare selection did not preserve the saved authorization: endpoint=%q account=%q zone=%q secret_changed=%v", endpoint, accountID, zoneID, currentSecretID != previousSecretID)
	}
}

func TestSwitchSystemDomainRequiresStoppedAccessPoints(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	storeSystemCenterCertificateForTest(t, store, "center.vastora.old.example.com")
	siteID := testSiteID(t, store)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, statement := range []string{
		`INSERT INTO settings(key, value) VALUES('agent_connection_mode', 'headscale'), ('agent_connect_url', 'https://center.vastora.old.example.com')`,
		`INSERT INTO network_integrations(kind, mode, endpoint, status, created_at, updated_at) VALUES('headscale', 'builtin', 'https://headscale.vastora.old.example.com', 'configured', '` + now + `', '` + now + `')`,
		`INSERT INTO agents(id, name, credential_hash, version, status, enrolled_at, last_seen_at, site_id) VALUES('node', 'Node', X'01', 'test', 'active', '` + now + `', '` + now + `', '` + siteID + `')`,
		`INSERT INTO applications(id, name, node_id, site_id, app_key, status, created_at, updated_at) VALUES('app', 'App', 'node', '` + siteID + `', 'test/app', 'running', '` + now + `', '` + now + `')`,
		`INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, status, created_at, updated_at) VALUES('service', 'app', '` + siteID + `', 'web', 'http', 80, 8080, '127.0.0.1:8080', 'catalog', 'ready', '` + now + `', '` + now + `')`,
		`INSERT INTO publications(id, service_id, kind, hostname, dns_provider, status, created_at, updated_at) VALUES('publication', 'service', 'headscale_gateway', 'app.example.com', 'headscale', 'ready', '` + now + `', '` + now + `')`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	server := NewServer(store, "", false).WithInfrastructureManager(&fakeBuiltinHeadscaleInstaller{})
	_, err = server.SwitchSystemDomain(ctx, SystemDomainSwitchInput{ZoneID: "missing", Confirm: true})
	if err == nil || !strings.Contains(err.Error(), "stop all access points") {
		t.Fatalf("active publication was accepted: %v", err)
	}
}
