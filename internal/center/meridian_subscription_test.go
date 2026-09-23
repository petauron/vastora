package center

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/networking"
)

func TestMeridianSubscriptionIsRenderedByCenterAuthority(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node := enrollOrchestrationNode(t, store, "entry-alpha", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "100.64.0.21", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.21", HeadscaleAddress: "100.64.0.21", EnabledKinds: []string{networking.KindHeadscale}})
	const (
		applicationID = "entry-alpha"
		serviceID     = "service-alpha"
		endpointID    = "endpoint-alpha"
		accountID     = "account-alpha"
		credentialID  = "credential-alpha"
		token         = "subscription-token-alpha"
		protocolID    = "11111111-1111-4111-8111-111111111111"
	)
	ctx := context.Background()
	siteID := testSiteID(t, store)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := store.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if _, err := tx.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at) VALUES(?,?,?,?,?,'','running','docker','',?,?)`, applicationID, "Entry Alpha", node.ID, siteID, meridianAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,display_name,region_code,protocol,container_port,host_port,endpoint,source,app_protocol,management,observed_listen,status,created_at,updated_at) VALUES(?,?,?,?,?,?,'tcp',443,443,?,'observed',?,0,'0.0.0','ready',?,?)`, serviceID, applicationID, siteID, "inbound-1", "🇺🇸 United States｜Entry Alpha", "US", "100.64.0.21:443", meridianEntryProtocol, now, now); err != nil {
		t.Fatal(err)
	}
	tokenSecretID, err := store.putSecret(ctx, tx, []byte(token), meridianAccountSecretContext(accountID))
	if err != nil {
		t.Fatal(err)
	}
	credentialSecretID, err := store.putSecret(ctx, tx, []byte(protocolID), meridianCredentialSecretContext(credentialID))
	if err != nil {
		t.Fatal(err)
	}
	endpointSecretID, err := store.putSecret(ctx, tx, []byte("test-private-key"), meridianEndpointSecretContext(endpointID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_endpoints(id,application_id,service_id,inbound_tag,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,fingerprint,desired_revision,applied_revision,runtime_healthy,status,created_at,updated_at) VALUES(?,?,?,?,443,'entry.example.test',443,'www.example.com:443','203.0.113.20','["www.example.com"]',?,'test-public-key','["abcd"]','chrome',1,1,1,'ready',?,?)`, endpointID, applicationID, serviceID, "meridian-entry-alpha", endpointSecretID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,sni_hostname,dns_provider,tls_enabled,desired_revision,applied_revision,status,created_at,updated_at)
		VALUES('entry-alpha-publication',?,'public_shared_443','application_node',?,'entry.example.test','www.example.com','manual',0,1,1,'ready',?,?)`, serviceID, node.ID, now, now); err != nil {
		t.Fatal(err)
	}
	digest := tokenHash(token)
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_accounts(id,display_name,total_bytes,expiry_time,reset_days,enabled,subscription_token_secret_id,subscription_token_sha256,desired_revision,applied_revision,status,created_at,updated_at) VALUES(?,?,1000,0,0,1,?,?,1,1,'active',?,?)`, accountID, "Account Alpha", tokenSecretID, hex.EncodeToString(digest), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_credentials(id,account_id,endpoint_id,kind,user_name,identity_sha256,protocol_secret_id,enabled,created_at,updated_at) VALUES(?,?,?,'native','entry-user',?,?,1,?,?)`, credentialID, accountID, endpointID, meridian.Identity(protocolID), credentialSecretID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_usage_watermarks(credential_id,baseline_bytes,observed_bytes,observed_at) VALUES(?,0,100,?)`, credentialID, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MeridianSubscription(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err := verifyCenterEncryptedState(ctx, store.db, store.key); err != nil {
		t.Fatalf("Meridian encrypted state was not owned by the database key verifier: %v", err)
	}
	// Subscription rendering uses only the public endpoint projection. Runtime
	// private keys stay outside the HTTP request path even if their encrypted
	// record is temporarily unreadable.
	if _, err := store.db.ExecContext(ctx, `UPDATE secrets SET sealed=X'00' WHERE id=?`, endpointSecretID); err != nil {
		t.Fatal(err)
	}

	server := NewServer(store, t.TempDir(), false)
	request := httptest.NewRequest(http.MethodGet, "/sub/"+token, nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Subscription-Userinfo") != "upload=0; download=100; total=1000" {
		t.Fatalf("subscription status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Referrer-Policy") != "no-referrer" || !strings.Contains(response.Header().Get("X-Robots-Tag"), "noindex") {
		t.Fatalf("subscription secret-safety headers=%v", response.Header())
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(response.Body.String()))
	if err != nil || !strings.Contains(string(decoded), "vless://"+protocolID+"@entry.example.test:443") {
		t.Fatalf("subscription body=%q err=%v", decoded, err)
	}
	var sealedSnapshot []byte
	if err := store.db.QueryRowContext(ctx, `SELECT secret.sealed FROM meridian_subscription_snapshots snapshot JOIN secrets secret ON secret.id=snapshot.secret_id WHERE snapshot.account_id=?`, accountID).Scan(&sealedSnapshot); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealedSnapshot, []byte(token)) || bytes.Contains(sealedSnapshot, []byte(protocolID)) || bytes.Contains(sealedSnapshot, []byte("vless://")) {
		t.Fatal("applied Meridian subscription snapshot was stored in plaintext")
	}

	mihomoRequest := httptest.NewRequest(http.MethodGet, "/sub/"+token+"?target=clash", nil)
	mihomoResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(mihomoResponse, mihomoRequest)
	if mihomoResponse.Code != http.StatusOK || !strings.Contains(mihomoResponse.Body.String(), "type: vless") || !strings.Contains(mihomoResponse.Body.String(), protocolID) {
		t.Fatalf("Mihomo status=%d body=%s", mihomoResponse.Code, mihomoResponse.Body.String())
	}

	if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, landingSelectionKey, `{"revision":0,"nodeIds":null}`); err != nil {
		t.Fatal(err)
	}
	independent := httptest.NewRecorder()
	server.Handler().ServeHTTP(independent, httptest.NewRequest(http.MethodGet, "/sub/"+token, nil))
	if independent.Code != http.StatusOK {
		t.Fatalf("native subscription depended on global landing metadata: status=%d body=%s", independent.Code, independent.Body.String())
	}

	// The authority route is moved to Center before any legacy runtime is
	// replaced. During that bounded phase the immutable imported identity set
	// remains available even though ordinary applied receipts are intentionally
	// still pending.
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_cutover SET state='publish',subscription_authority='meridian',import_sha256=? WHERE id=1`, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_accounts SET applied_revision=0 WHERE id=?`, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_endpoints SET applied_revision=0,runtime_healthy=0,status='pending' WHERE id=?`, endpointID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE services SET status='pending' WHERE id=?`, serviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE publications SET desired_revision=2,applied_revision=1,status='pending' WHERE service_id=?`, serviceID); err != nil {
		t.Fatal(err)
	}
	snapshot := httptest.NewRecorder()
	server.Handler().ServeHTTP(snapshot, httptest.NewRequest(http.MethodGet, "/sub/"+token, nil))
	if snapshot.Code != http.StatusOK {
		t.Fatalf("imported cutover snapshot status=%d body=%s", snapshot.Code, snapshot.Body.String())
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_cutover SET state='complete' WHERE id=1;
		UPDATE meridian_accounts SET desired_revision=2,applied_revision=1 WHERE id=?;
		UPDATE meridian_endpoints SET desired_revision=2,applied_revision=1 WHERE id=?`, accountID, endpointID); err != nil {
		t.Fatal(err)
	}
	recovered := httptest.NewRecorder()
	server.Handler().ServeHTTP(recovered, httptest.NewRequest(http.MethodGet, "/sub/"+token, nil))
	if recovered.Code != http.StatusOK || recovered.Body.String() != response.Body.String() {
		t.Fatalf("pending runtime did not preserve last applied subscription: status=%d body=%s", recovered.Code, recovered.Body.String())
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_accounts SET enabled=0,status='disabled' WHERE id=?`, accountID); err != nil {
		t.Fatal(err)
	}
	revoked := httptest.NewRecorder()
	server.Handler().ServeHTTP(revoked, httptest.NewRequest(http.MethodGet, "/sub/"+token, nil))
	if revoked.Code != http.StatusNotFound {
		t.Fatalf("disabled account served an applied snapshot: status=%d body=%s", revoked.Code, revoked.Body.String())
	}

	missing := httptest.NewRecorder()
	server.Handler().ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/sub/not-the-token", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing token status=%d", missing.Code)
	}
	if missing.Header().Get("Cache-Control") != "no-store" || missing.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("missing subscription token response was cacheable: headers=%v", missing.Header())
	}
}

func TestUnavailableMeridianLandingBlocksOnlyItsFixedRoute(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	entry := enrollOrchestrationNode(t, store, "route-filter-entry", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "100.64.0.71", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.71", HeadscaleAddress: "100.64.0.71", EnabledKinds: []string{networking.KindHeadscale}})
	egress := enrollOrchestrationNode(t, store, "route-filter-egress", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "100.64.0.72", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.72", HeadscaleAddress: "100.64.0.72", EnabledKinds: []string{networking.KindHeadscale}})
	ctx := context.Background()
	siteID := testSiteID(t, store)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := store.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	const (
		applicationID = "route-filter-application"
		serviceID     = "route-filter-service"
		endpointID    = "route-filter-endpoint"
		accountID     = "route-filter-account"
		baseID        = "route-filter-base"
		routeID       = "route-filter-routed"
		grantID       = "route-filter-grant"
		baseSecret    = "11111111-1111-4111-8111-111111111111"
		routeSecret   = "22222222-2222-4222-8222-222222222222"
	)
	if _, err := tx.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES(?,?,?,?,?,'','running','docker','',?,?)`, applicationID, "Route filter entry", entry.ID, siteID, meridianAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,display_name,protocol,container_port,host_port,endpoint,source,app_protocol,management,observed_listen,status,created_at,updated_at)
		VALUES(?,?,?,?,?,'tcp',443,443,?,'observed',?,0,'0.0.0','ready',?,?)`, serviceID, applicationID, siteID, "inbound-1", "Route filter entry", "100.64.0.71:443", meridianEntryProtocol, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,sni_hostname,dns_provider,tls_enabled,desired_revision,applied_revision,status,created_at,updated_at)
		VALUES('route-filter-publication',?,'public_shared_443','application_node',?,'entry.example.test','www.example.com','manual',0,1,1,'ready',?,?)`, serviceID, entry.ID, now, now); err != nil {
		t.Fatal(err)
	}
	endpointSecretID, err := store.putSecret(ctx, tx, []byte("route-filter-private-key"), meridianEndpointSecretContext(endpointID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_endpoints(id,application_id,service_id,inbound_tag,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,fingerprint,desired_revision,applied_revision,runtime_healthy,status,created_at,updated_at)
		VALUES(?,?,?,?,443,'entry.example.test',443,'www.example.com:443','203.0.113.20','["www.example.com"]',?,'route-filter-public-key','["abcd"]','chrome',1,1,1,'ready',?,?)`, endpointID, applicationID, serviceID, "route-filter-inbound", endpointSecretID, now, now); err != nil {
		t.Fatal(err)
	}
	tokenSecretID, err := store.putSecret(ctx, tx, []byte("route-filter-token"), meridianAccountSecretContext(accountID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_accounts(id,display_name,subscription_token_secret_id,subscription_token_sha256,desired_revision,applied_revision,status,created_at,updated_at)
		VALUES(?,?,?, ?,1,1,'active',?,?)`, accountID, "Route filter account", tokenSecretID, meridian.SubscriptionTokenFingerprint("route-filter-token"), now, now); err != nil {
		t.Fatal(err)
	}
	baseSecretID, err := store.putSecret(ctx, tx, []byte(baseSecret), meridianCredentialSecretContext(baseID))
	if err != nil {
		t.Fatal(err)
	}
	routeSecretID, err := store.putSecret(ctx, tx, []byte(routeSecret), meridianCredentialSecretContext(routeID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_credentials(id,account_id,endpoint_id,kind,user_name,identity_sha256,protocol_secret_id,enabled,created_at,updated_at)
		VALUES(?,?,?,'native','route-filter-native',?,?,1,?,?)`, baseID, accountID, endpointID, meridian.Identity(baseSecret), baseSecretID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_credentials(id,account_id,endpoint_id,kind,user_name,identity_sha256,protocol_secret_id,egress_node_id,enabled,created_at,updated_at)
		VALUES(?,?,?,'route',?,?,?,?,1,?,?)`, routeID, accountID, endpointID, meridian.RouteUser(grantID), meridian.Identity(routeSecret), routeSecretID, egress.ID, now, now); err != nil {
		t.Fatal(err)
	}
	serverJSON, _ := meridianAppliedLandingFixtureJSON(t, egress.ID, "100.64.0.72", "100.64.0.71")
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET source_peer_json=? WHERE id=?`, []byte(`{"id":"tailnet-filter-entry","publicKey":"nodekey:test-filter-entry","address":"100.64.0.71"}`), endpointID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO landing_server_states(node_id,desired_revision,applied_revision,desired_json,applied_json,peer_json,status,last_error,updated_at)
		VALUES(?,1,0,?,'{}','{}','failed','landing unavailable',?)`, egress.ID, serverJSON, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_route_grants(id,account_id,endpoint_id,egress_node_id,base_credential_id,route_credential_id,hide_native,desired_revision,applied_revision,runtime_healthy,status,last_error,created_at,updated_at)
		VALUES(?,?,?,?,?,?,1,1,1,0,'blocked',?, ?,?)`, grantID, accountID, endpointID, egress.ID, baseID, routeID, meridianRoutePeerUnavailable, now, now); err != nil {
		t.Fatal(err)
	}

	base := meridian.Credential{ID: baseID, AccountID: accountID, Kind: meridian.NativeCredential, User: "route-filter-native", Identity: meridian.Identity(baseSecret), EntryID: applicationID, Enabled: true}
	routed := meridian.Credential{ID: routeID, AccountID: accountID, Kind: meridian.RouteCredential, User: meridian.RouteUser(grantID), Identity: meridian.Identity(routeSecret), EntryID: applicationID, EgressID: egress.ID, Enabled: true}
	runtimeRoutes, err := store.meridianRuntimeGrants(ctx, tx, endpointID, "route-filter-inbound", map[string]meridian.CredentialMaterial{
		baseID:  {Credential: base, ProtocolID: baseSecret},
		routeID: {Credential: routed, ProtocolID: routeSecret},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(runtimeRoutes.grants) != 0 || runtimeRoutes.blockedGrants[grantID] != meridianRoutePeerUnavailable || !runtimeRoutes.disabledCredentialIDs[routeID] {
		t.Fatalf("unavailable landing projection = %#v", runtimeRoutes)
	}
	baseLink := "vless://" + baseSecret + "@entry.example.test:443?security=reality&type=tcp&sni=www.example.com&pbk=public&sid=abcd&fp=chrome&flow=xtls-rprx-vision#Entry"
	routeLink := "vless://" + routeSecret + "@entry.example.test:443?security=reality&type=tcp&sni=www.example.com&pbk=public&sid=abcd&fp=chrome&flow=xtls-rprx-vision#Entry"
	grant := meridian.RouteGrant{ID: grantID, AccountID: accountID, EntryID: applicationID, EgressID: egress.ID, InboundTag: "route-filter-inbound", Base: base, Route: routed, Mode: meridian.FixedMode, Enabled: true, HideNative: true, DesiredRev: 1, AppliedRev: 1, RuntimeGood: true}
	snapshot := meridianAppliedSubscriptionSnapshot{AccountID: accountID, AccountRevision: 1,
		Entries: []meridian.NativeEntry{{Material: meridian.CredentialMaterial{Credential: base, ProtocolID: baseSecret}, Protocol: meridian.VLESSReality, Link: baseLink}},
		Routes:  []meridian.PublishedRoute{{Grant: grant, Protocol: meridian.VLESSReality, EntryName: "Entry", BaseLink: baseLink, RouteLink: routeLink, BaseProtocolIdentity: base.Identity, RouteProtocolIdentity: routed.Identity}},
	}
	filtered, err := store.filterMeridianSubscriptionSnapshotRoutesInTx(ctx, tx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Routes) != 0 || len(filtered.Entries) != 0 {
		t.Fatalf("hidden fixed route did not fail closed: %#v", filtered)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET hide_native=0 WHERE id=?`, grantID); err != nil {
		t.Fatal(err)
	}
	filtered, err = store.filterMeridianSubscriptionSnapshotRoutesInTx(ctx, tx, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Routes) != 0 || len(filtered.Entries) != 1 || filtered.Entries[0].Material.Credential.ID != baseID {
		t.Fatalf("independent native entry was removed with unavailable route: %#v", filtered)
	}
}

func TestMeridianQuotaBoundaryRebuildsEveryAccountEndpoint(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	entryA := enrollOrchestrationNode(t, store, "entry-a", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "100.64.0.31", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.31", HeadscaleAddress: "100.64.0.31", EnabledKinds: []string{networking.KindHeadscale}})
	entryB := enrollOrchestrationNode(t, store, "entry-b", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "100.64.0.32", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.32", HeadscaleAddress: "100.64.0.32", EnabledKinds: []string{networking.KindHeadscale}})
	egress := enrollOrchestrationNode(t, store, "egress", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "100.64.0.33", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.33", HeadscaleAddress: "100.64.0.33", EnabledKinds: []string{networking.KindHeadscale}})

	ctx := context.Background()
	siteID := testSiteID(t, store)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := store.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	accountID := "shared-account"
	tokenSecretID, err := store.putSecret(ctx, tx, []byte("shared-token"), meridianAccountSecretContext(accountID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_accounts(id,display_name,total_bytes,expiry_time,reset_days,enabled,subscription_token_secret_id,subscription_token_sha256,desired_revision,applied_revision,status,created_at,updated_at)
		VALUES(?, 'Shared account', 100, 0, 0, 1, ?, ?, 1, 1, 'active', ?, ?)`, accountID, tokenSecretID, meridian.SubscriptionTokenFingerprint("shared-token"), now, now); err != nil {
		t.Fatal(err)
	}

	insertEndpoint := func(applicationID, serviceID, endpointID string, nodeID string) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
			VALUES(?,?,?,?,?,'','running','docker','',?,?) ON CONFLICT(id) DO NOTHING`, applicationID, applicationID, nodeID, siteID, meridianAppKey, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,display_name,protocol,container_port,host_port,endpoint,source,app_protocol,management,observed_listen,status,created_at,updated_at)
			VALUES(?,?,?,?,?,'tcp',443,443,?,'observed',?,0,'0.0.0','ready',?,?)`, serviceID, applicationID, siteID, "inbound-"+endpointID, applicationID, "100.64.0.31:443", meridianEntryProtocol, now, now); err != nil {
			t.Fatal(err)
		}
		endpointSecretID, err := store.putSecret(ctx, tx, []byte("private-"+endpointID), meridianEndpointSecretContext(endpointID))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_endpoints(id,application_id,service_id,inbound_tag,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,fingerprint,desired_revision,applied_revision,runtime_healthy,status,created_at,updated_at)
			VALUES(?,?,?,?,443,?,443,'www.example.com:443','203.0.113.20','["www.example.com"]',?,'public','["abcd"]','chrome',1,1,1,'ready',?,?)`, endpointID, applicationID, serviceID, "inbound-"+endpointID, endpointID+".example.test", endpointSecretID, now, now); err != nil {
			t.Fatal(err)
		}
		baseID, routeID := "base-"+endpointID, "route-"+endpointID
		baseProtocolID := "11111111-1111-4111-8111-" + strings.Repeat("1", 12)
		routeProtocolID := "22222222-2222-4222-8222-" + strings.Repeat("2", 12)
		baseSecretID, err := store.putSecret(ctx, tx, []byte(baseProtocolID), meridianCredentialSecretContext(baseID))
		if err != nil {
			t.Fatal(err)
		}
		routeSecretID, err := store.putSecret(ctx, tx, []byte(routeProtocolID), meridianCredentialSecretContext(routeID))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_credentials(id,account_id,endpoint_id,kind,user_name,identity_sha256,protocol_secret_id,enabled,created_at,updated_at)
			VALUES(?,?,?,'native',?,?,?,1,?,?)`, baseID, accountID, endpointID, "user-"+baseID, meridian.Identity(baseProtocolID), baseSecretID, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_credentials(id,account_id,endpoint_id,kind,user_name,identity_sha256,protocol_secret_id,egress_node_id,enabled,created_at,updated_at)
			VALUES(?,?,?,'route',?,?,?,?,1,?,?)`, routeID, accountID, endpointID, "user-"+routeID, meridian.Identity(routeProtocolID), routeSecretID, egress.ID, now, now); err != nil {
			t.Fatal(err)
		}
		for _, credentialID := range []string{baseID, routeID} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_usage_watermarks(credential_id,observed_at) VALUES(?,?)`, credentialID, now); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_route_grants(id,account_id,endpoint_id,egress_node_id,base_credential_id,route_credential_id,desired_revision,applied_revision,runtime_healthy,status,health_expires_unix_ms,created_at,updated_at)
			VALUES(?,?,?,?,?,?,1,1,1,'ready',?,?,?)`, "grant-"+endpointID, accountID, endpointID, egress.ID, baseID, routeID, store.now().Add(10*time.Second).UnixMilli(), now, now); err != nil {
			t.Fatal(err)
		}
	}
	insertEndpoint("application-a", "service-a", "endpoint-a", entryA.ID)
	insertEndpoint("application-b", "service-b", "endpoint-b", entryB.ID)
	// Distinct physical entries must not describe the same public transport.
	// Reusing the UUID across entries is valid; duplicating host/SNI/REALITY is not.
	for _, publication := range []struct{ serviceID, endpointID, nodeID string }{
		{"service-a", "endpoint-a", entryA.ID},
		{"service-b", "endpoint-b", entryB.ID},
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,sni_hostname,dns_provider,tls_enabled,desired_revision,applied_revision,status,created_at,updated_at)
			VALUES(?,?,'public_shared_443','application_node',?,?,'www.example.com','manual',0,1,1,'ready',?,?)`, "publication-"+publication.serviceID, publication.serviceID, publication.nodeID, publication.endpointID+".example.test", now, now); err != nil {
			t.Fatal(err)
		}
	}
	// A retired endpoint still owns its application identity. Use a separate
	// installation instead of violating the one-endpoint-per-application key.
	insertEndpoint("application-retired", "service-retired", "endpoint-retired", egress.ID)
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_credentials SET enabled=0 WHERE endpoint_id='endpoint-retired'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET enabled=0,status='revoked' WHERE endpoint_id='endpoint-retired'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET runtime_healthy=0,status='retired' WHERE id='endpoint-retired'`); err != nil {
		t.Fatal(err)
	}

	// A different account shares the same runtime but has never downloaded its
	// subscription. Exhausting the first account must not withdraw this account.
	const (
		otherAccountID    = "independent-account"
		otherToken        = "independent-token"
		otherCredentialID = "independent-base-a"
		otherProtocolID   = "33333333-3333-4333-8333-333333333333"
	)
	otherTokenSecretID, err := store.putSecret(ctx, tx, []byte(otherToken), meridianAccountSecretContext(otherAccountID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_accounts(id,display_name,total_bytes,enabled,subscription_token_secret_id,subscription_token_sha256,desired_revision,applied_revision,status,created_at,updated_at)
		VALUES(?, 'Independent account', 100, 1, ?, ?, 1, 1, 'active', ?, ?)`, otherAccountID, otherTokenSecretID, meridian.SubscriptionTokenFingerprint(otherToken), now, now); err != nil {
		t.Fatal(err)
	}
	otherCredentialSecretID, err := store.putSecret(ctx, tx, []byte(otherProtocolID), meridianCredentialSecretContext(otherCredentialID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_credentials(id,account_id,endpoint_id,kind,user_name,identity_sha256,protocol_secret_id,enabled,created_at,updated_at)
		VALUES(?,?,'endpoint-a','native','independent-user',?,?,1,?,?)`, otherCredentialID, otherAccountID, meridian.Identity(otherProtocolID), otherCredentialSecretID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_usage_watermarks(credential_id,observed_bytes,observed_at) VALUES(?,5,?)`, otherCredentialID, now); err != nil {
		t.Fatal(err)
	}
	var initialSnapshots int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_subscription_snapshots WHERE account_id IN (?,?)`, accountID, otherAccountID).Scan(&initialSnapshots); err != nil || initialSnapshots != 0 {
		t.Fatalf("subscriptions were already snapshotted: count=%d err=%v", initialSnapshots, err)
	}
	quotaBefore, err := store.meridianQuotaStatesInTx(ctx, tx, "endpoint-a")
	if err != nil || !quotaBefore[accountID] || !quotaBefore[otherAccountID] {
		t.Fatalf("accounts were not initially within quota: states=%v err=%v", quotaBefore, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_usage_watermarks SET observed_bytes=100 WHERE credential_id='base-endpoint-a'`); err != nil {
		t.Fatal(err)
	}
	quotaAfter, err := store.meridianQuotaStatesInTx(ctx, tx, "endpoint-a")
	if err != nil || quotaAfter[accountID] || !quotaAfter[otherAccountID] {
		t.Fatalf("quota crossing affected the wrong accounts: states=%v err=%v", quotaAfter, err)
	}
	if err := store.markMeridianQuotaBoundaryChanged(ctx, tx, []string{accountID}, now); err != nil {
		t.Fatal(err)
	}
	// No subscription download preceded the usage-driven revision change.
	// Its last applied entries must still be recoverable during the rebuild.
	snapshot, err := store.loadMeridianSubscriptionSnapshotInTx(ctx, tx, accountID)
	if err != nil || snapshot.AccountRevision != 1 || len(snapshot.Entries) != 2 {
		t.Fatalf("quota boundary lost applied subscription: revision=%d entries=%d err=%v", snapshot.AccountRevision, len(snapshot.Entries), err)
	}
	for _, endpointID := range []string{"endpoint-a", "endpoint-b"} {
		var desiredRevision, healthy int
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT desired_revision,runtime_healthy,status FROM meridian_endpoints WHERE id=?`, endpointID).Scan(&desiredRevision, &healthy, &status); err != nil {
			t.Fatal(err)
		}
		if desiredRevision != 2 || healthy != 0 || status != "pending" {
			t.Fatalf("endpoint %s did not receive shared quota revision: revision=%d healthy=%d status=%s", endpointID, desiredRevision, healthy, status)
		}
		var grantRevision, grantHealthy int
		if err := tx.QueryRowContext(ctx, `SELECT desired_revision,runtime_healthy FROM meridian_route_grants WHERE endpoint_id=?`, endpointID).Scan(&grantRevision, &grantHealthy); err != nil {
			t.Fatal(err)
		}
		if grantRevision != 2 || grantHealthy != 0 {
			t.Fatalf("endpoint %s route did not receive shared quota revision: revision=%d healthy=%d", endpointID, grantRevision, grantHealthy)
		}
	}
	var retiredRevision int
	var retiredStatus string
	if err := tx.QueryRowContext(ctx, `SELECT desired_revision,status FROM meridian_endpoints WHERE id='endpoint-retired'`).Scan(&retiredRevision, &retiredStatus); err != nil {
		t.Fatal(err)
	}
	if retiredRevision != 1 || retiredStatus != "retired" {
		t.Fatalf("quota boundary resurrected retired endpoint: revision=%d status=%s", retiredRevision, retiredStatus)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	server := NewServer(store, t.TempDir(), false)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sub/"+otherToken, nil))
	decoded, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(response.Body.String()))
	if response.Code != http.StatusOK || decodeErr != nil || !strings.Contains(string(decoded), "vless://"+otherProtocolID+"@endpoint-a.example.test:443") {
		t.Fatalf("shared runtime rebuild withdrew unaffected subscription: status=%d body=%q err=%v", response.Code, decoded, decodeErr)
	}
	if _, err := store.MeridianSubscription(ctx, "shared-token"); !errors.Is(err, errMeridianSubscriptionNotFound) {
		t.Fatalf("applied snapshot bypassed exhausted account quota: %v", err)
	}
	enabled := true
	if _, err := store.UpdateMeridianAccount(ctx, accountID, MeridianAccountInput{DisplayName: "Shared account", TotalBytes: 100, Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision,status FROM meridian_endpoints WHERE id='endpoint-retired'`).Scan(&retiredRevision, &retiredStatus); err != nil {
		t.Fatal(err)
	}
	if retiredRevision != 1 || retiredStatus != "retired" {
		t.Fatalf("account update resurrected retired endpoint: revision=%d status=%s", retiredRevision, retiredStatus)
	}
	peerTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.markMeridianLandingPeerChanged(ctx, peerTx, egress.ID, now); err != nil {
		peerTx.Rollback()
		t.Fatal(err)
	}
	if err := peerTx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, endpointID := range []string{"endpoint-a", "endpoint-b"} {
		var revision int
		if err := store.db.QueryRowContext(ctx, `SELECT desired_revision FROM meridian_endpoints WHERE id=?`, endpointID).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if revision != 4 {
			t.Fatalf("landing peer change did not rebuild endpoint %s: revision=%d", endpointID, revision)
		}
	}
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision,status FROM meridian_endpoints WHERE id='endpoint-retired'`).Scan(&retiredRevision, &retiredStatus); err != nil {
		t.Fatal(err)
	}
	if retiredRevision != 1 || retiredStatus != "retired" {
		t.Fatalf("landing peer change resurrected retired endpoint: revision=%d status=%s", retiredRevision, retiredStatus)
	}
}

func TestInvalidMeridianRuntimeDoesNotBlockAgentClaims(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node := enrollOrchestrationNode(t, store, "entry-blocked", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "100.64.0.41", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.41", HeadscaleAddress: "100.64.0.41", EnabledKinds: []string{networking.KindHeadscale}})
	ctx := context.Background()
	siteID := testSiteID(t, store)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := store.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	// An empty credential set is deployable. An installation with no audited
	// image is not; its failure must release the claim loop for other work.
	if _, err := tx.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES('blocked-application','Blocked entry',?,?,?,'','running','docker','',?,?)`, node.ID, siteID, meridianAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,display_name,protocol,container_port,host_port,endpoint,source,app_protocol,management,observed_listen,status,created_at,updated_at)
		VALUES('blocked-service','blocked-application',?,'inbound-1','Blocked entry','tcp',443,443,'100.64.0.41:443','observed',?,0,'0.0.0','pending',?,?)`, siteID, meridianEntryProtocol, now, now); err != nil {
		t.Fatal(err)
	}
	secretID, err := store.putSecret(ctx, tx, []byte("blocked-private-key"), meridianEndpointSecretContext("blocked-endpoint"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_endpoints(id,application_id,service_id,inbound_tag,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,fingerprint,desired_revision,applied_revision,runtime_healthy,status,created_at,updated_at)
		VALUES('blocked-endpoint','blocked-application','blocked-service','blocked-tag',443,'entry.example.test',443,'www.example.com:443','203.0.113.20','["www.example.com"]',?,'blocked-public-key','["abcd"]','chrome',1,0,0,'pending',?,?)`, secretID, now, now); err != nil {
		t.Fatal(err)
	}

	err = store.queueNextMeridianRuntime(ctx, tx, node.ID)
	if !errors.Is(err, errApplicationCommandDiscarded) {
		t.Fatalf("undeployable projection did not release the claim loop: %v", err)
	}
	var status, lastError string
	if err := tx.QueryRowContext(ctx, `SELECT status,last_error FROM meridian_endpoints WHERE id='blocked-endpoint'`).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || !strings.Contains(lastError, "runtime task is invalid") {
		t.Fatalf("undeployable endpoint status=%s error=%q", status, lastError)
	}
}

func TestSupersededMeridianReceiptLeavesNewestRevisionPending(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node := enrollOrchestrationNode(t, store, "entry-superseded", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "100.64.0.42", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.42", HeadscaleAddress: "100.64.0.42", EnabledKinds: []string{networking.KindHeadscale}})
	ctx := context.Background()
	now := store.now().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	siteID := testSiteID(t, store)
	queuedSHA := meridian.Identity("superseded-queued-config")
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES('superseded-application','Superseded entry',?,?,?,'','running','docker','',?,?)`, node.ID, siteID, meridianAppKey, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,display_name,protocol,container_port,host_port,endpoint,source,app_protocol,management,observed_listen,status,created_at,updated_at)
		VALUES('superseded-service','superseded-application',?,'inbound-1','Superseded entry','tcp',443,443,'100.64.0.42:443','observed',?,0,'0.0.0','pending',?,?)`, siteID, meridianEntryProtocol, now, now); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	secretID, err := store.putSecret(ctx, tx, []byte("superseded-private-key"), meridianEndpointSecretContext("superseded-endpoint"))
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO meridian_endpoints(id,application_id,service_id,inbound_tag,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,fingerprint,desired_revision,applied_revision,runtime_healthy,status,created_at,updated_at)
		VALUES('superseded-endpoint','superseded-application','superseded-service','superseded-tag',443,'entry.example.test',443,'www.example.com:443','203.0.113.20','["www.example.com"]',?,'superseded-public-key','["abcd"]','chrome',2,0,0,'pending',?,?)`, secretID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,site_id,display_name,agent_id,gateway_node_id,kind,input_json,state,attempt,created_at,updated_at)
		VALUES('superseded-queued-command','superseded-application',?,'Superseded entry',?,?,?,?,'pending',0,?,?)`, siteID, node.ID, node.ID, meridianruntime.ApplyKind, []byte(`{"endpointId":"superseded-endpoint"}`), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO meridian_deployments(endpoint_id,desired_revision,desired_sha256,command_id,status,updated_at)
		VALUES('superseded-endpoint',1,?,'superseded-queued-command','pending',?)`, queuedSHA, now); err != nil {
		t.Fatal(err)
	}
	claimTx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if task, claimErr := store.claimApplicationCommand(ctx, claimTx, node.ID); task != nil || !errors.Is(claimErr, errApplicationCommandDiscarded) {
		claimTx.Rollback()
		t.Fatalf("superseded queued command was not discarded: task=%#v err=%v", task, claimErr)
	}
	if err := claimTx.Commit(); err != nil {
		t.Fatal(err)
	}
	var endpointStatus, commandState, event string
	var desiredRevision int
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision,status FROM meridian_endpoints WHERE id='superseded-endpoint'`).Scan(&desiredRevision, &endpointStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT state FROM application_commands WHERE id='superseded-queued-command'`).Scan(&commandState); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT event FROM task_events WHERE task_id='superseded-queued-command' ORDER BY created_at DESC LIMIT 1`).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if desiredRevision != 2 || endpointStatus != "pending" || commandState != "succeeded" || event != "succeeded" {
		t.Fatalf("superseded queued command changed newest desired state: revision=%d endpoint=%s command=%s event=%s", desiredRevision, endpointStatus, commandState, event)
	}

	completionSHA := meridian.Identity("superseded-completion-config")
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_endpoints SET desired_revision=3 WHERE id='superseded-endpoint'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,site_id,display_name,agent_id,gateway_node_id,kind,input_json,state,attempt,created_at,updated_at)
		VALUES('superseded-completion-command','superseded-application',?,'Superseded entry',?,?,?,?,'running',1,?,?)`, siteID, node.ID, node.ID, meridianruntime.ApplyKind, []byte(`{"endpointId":"superseded-endpoint"}`), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_deployments SET desired_revision=2,desired_sha256=?,command_id='superseded-completion-command',status='applying',last_error='',updated_at=? WHERE endpoint_id='superseded-endpoint'`, completionSHA, now); err != nil {
		t.Fatal(err)
	}
	result := ApplicationTaskResult{MeridianRuntime: &meridianruntime.Result{Receipt: meridian.AppliedReceipt{Revision: 2, ConfigSHA256: completionSHA, RuntimeReady: true}, Stats: json.RawMessage(`{}`)}}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.completeApplicationCommand(ctx, commitProjectionOnlyForTest, node.ID, "superseded-completion-command", 1, true, "", raw, false); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision,status FROM meridian_endpoints WHERE id='superseded-endpoint'`).Scan(&desiredRevision, &endpointStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT state FROM application_commands WHERE id='superseded-completion-command'`).Scan(&commandState); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT event FROM task_events WHERE task_id='superseded-completion-command' ORDER BY created_at DESC LIMIT 1`).Scan(&event); err != nil {
		t.Fatal(err)
	}
	if desiredRevision != 3 || endpointStatus != "pending" || commandState != "succeeded" || event != "succeeded" {
		t.Fatalf("superseded completion changed newest desired state: revision=%d endpoint=%s command=%s event=%s", desiredRevision, endpointStatus, commandState, event)
	}
}
