package center

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

// Real verified HTTPS and backend switching around the generic retained route.
// This does not substitute for installed HAProxy/Glivela cutover acceptance.
func TestServiceHandoverPreservesPublicIdentityAndFencesConcurrentChanges(t *testing.T) {
	ctx := context.Background()
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	backend := func(label string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/sub/original-token" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("X-Backend", label)
			_, _ = io.WriteString(w, "retained-subscription-payload")
		}))
	}
	old, next := backend("old"), backend("new")
	defer old.Close()
	defer next.Close()
	profile := networking.Profile{ServiceAddress: "10.0.0.61", LANAddress: "10.0.0.61", PublicAddress: "203.0.113.61", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true}
	node := enrollOrchestrationNode(t, store, "service-handover-example", NodeCapabilities{Docker: true, Gateway: true}, []networking.Candidate{{Address: profile.ServiceAddress, Interface: "eth0", Kind: networking.KindLAN}, {Address: profile.PublicAddress, Interface: "eth0", Kind: networking.KindPublic}}, profile)
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	site := testSiteID(t, store)
	endpoints := []string{}
	for i, item := range []struct{ id, endpoint string }{{"source", old.Listener.Addr().String()}, {"target", next.Listener.Addr().String()}} {
		app, key := item.id+"-application", "example/"+item.id
		exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at) VALUES(?,?,?,?,?,'','running','docker','',?,?)`, app, item.id, node.ID, site, key, stamp, stamp)
		receipt, _ := json.Marshal(map[string]any{"version": 1, "applicationId": app, "appKey": key, "runtime": "docker", "manifestSha256": strings.Repeat("a", 64), "state": "ready", "resources": []any{map[string]any{"name": "synthetic-container"}}})
		exec(`INSERT INTO application_resources(application_id,package_revision,manifest_sha256,authorized_capabilities_json,resources_json,adoption_state,updated_at) VALUES(?,1,?,'[]',?,'ready',?)`, app, strings.Repeat("a", 64), receipt, stamp)
		_, port, err := net.SplitHostPort(item.endpoint)
		if err != nil {
			t.Fatal(err)
		}
		endpoint := net.JoinHostPort(profile.ServiceAddress, port)
		endpoints = append(endpoints, endpoint)
		source := "catalog"
		if i == 0 {
			source = "system"
		}
		exec(`INSERT INTO services(id,application_id,site_id,name,display_name,protocol,container_port,host_port,endpoint,source,management,status,created_at,updated_at) VALUES(?,?,?,'subscription','Original name','http',8080,8080,?,?,0,'ready',?,?)`, item.id+"-service", app, site, endpoint, source, stamp, stamp)
	}
	exec(`INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,dns_provider,dns_record_id,tls_enabled,desired_revision,applied_revision,status,created_at,updated_at) VALUES('original-publication','source-service','public_direct','site_gateway',?,'example.com','manual','original-dns-record',1,1,1,'ready',?,?)`, node.ID, stamp, stamp)
	exec(`INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,management,status,created_at,updated_at) VALUES('unrelated-service','target-application',?,'other','http',8081,8081,'10.0.0.61:8081','catalog',0,'ready',?,?)`, site, stamp, stamp)
	exec(`INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,dns_provider,tls_enabled,desired_revision,applied_revision,status,created_at,updated_at) VALUES('unrelated-publication','unrelated-service','public_direct','site_gateway',?,'other.example.com','manual',1,5,5,'ready',?,?)`, node.ID, stamp, stamp)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.upsertPublicationRoute(ctx, tx, "original-publication", site, "source-service", node.ID, "example.com", "http", endpoints[0], true, store.now()); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var routeID string
	if err := store.db.QueryRow(`SELECT id FROM routes WHERE publication_id='original-publication'`).Scan(&routeID); err != nil {
		t.Fatal(err)
	}
	front := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var upstream []byte
		if err := store.db.QueryRow(`SELECT upstreams_json FROM routes WHERE id=?`, routeID).Scan(&upstream); err != nil {
			http.Error(w, "route unavailable", http.StatusServiceUnavailable)
			return
		}
		var addresses []string
		if json.Unmarshal(upstream, &addresses) != nil || len(addresses) != 1 {
			http.Error(w, "route invalid", http.StatusServiceUnavailable)
			return
		}
		proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: addresses[0]})
		proxy.Transport = &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil || host != profile.ServiceAddress {
				return nil, errServiceHandover
			}
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
		}}
		proxy.ServeHTTP(w, r)
	}))
	defer front.Close()
	roots := x509.NewCertPool()
	roots.AddCert(front.Certificate())
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, front.Listener.Addr().String())
	}}}
	defer client.CloseIdleConnections()
	_, port, _ := net.SplitHostPort(front.Listener.Addr().String())
	publicURL := "https://example.com:" + port + "/sub/original-token"
	request := func(want string) {
		t.Helper()
		response, err := client.Get(publicURL)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != 200 || string(body) != "retained-subscription-payload" || response.Header.Get("X-Backend") != want || response.TLS == nil || len(response.TLS.VerifiedChains) == 0 {
			t.Fatal("public URL/token or verified TLS failed", err)
		}
	}
	request("old")
	plan, err := store.PreviewServiceHandover(ctx, "source-service", "target-service")
	if err != nil {
		t.Fatal(err)
	}
	input := ServiceHandover{TargetServiceID: plan.TargetServiceID, SourceSHA256: plan.SourceSHA256, TargetSHA256: plan.TargetSHA256, BackupsConfirmed: true}
	exec(`UPDATE services SET management=1 WHERE id='target-service'`)
	if _, err := store.PreviewServiceHandover(ctx, "source-service", "target-service"); err == nil {
		t.Fatal("access boundary mismatch accepted")
	}
	exec(`UPDATE services SET management=0 WHERE id='target-service'`)
	missingBackup := input
	missingBackup.BackupsConfirmed = false
	if _, err := store.HandoverService(ctx, "source-service", missingBackup); err == nil {
		t.Fatal("missing backup confirmation accepted")
	}
	exec(`UPDATE services SET updated_at='changed' WHERE id='target-service'`)
	if _, err := store.HandoverService(ctx, "source-service", input); err == nil {
		t.Fatal("stale target evidence accepted")
	}
	exec(`UPDATE services SET updated_at=? WHERE id='target-service'`, stamp)
	result, err := store.HandoverService(ctx, "source-service", input)
	if err != nil || result.State != "binding-updated-verification-pending" {
		t.Fatal(result, err)
	}
	request("new")
	var serviceID, hostname, dns, state, application, name, retainedRoute string
	var revision int64
	if err := store.db.QueryRow(`SELECT service_id,hostname,dns_record_id,status,desired_revision FROM publications WHERE id='original-publication'`).Scan(&serviceID, &hostname, &dns, &state, &revision); err != nil || serviceID != "source-service" || hostname != "example.com" || dns != "original-dns-record" || state != "pending" || revision != 2 {
		t.Fatal("public identity changed or binding was called healthy", err)
	}
	if err := store.db.QueryRow(`SELECT application_id,display_name FROM services WHERE id='source-service'`).Scan(&application, &name); err != nil || application != "target-application" || name != "Original name" {
		t.Fatal("service identity/name was not preserved", err)
	}
	if err := store.db.QueryRow(`SELECT id FROM routes WHERE publication_id='original-publication'`).Scan(&retainedRoute); err != nil || retainedRoute != routeID {
		t.Fatal("route identity changed", err)
	}
	if err := store.db.QueryRow(`SELECT desired_revision,status FROM publications WHERE id='unrelated-publication'`).Scan(&revision, &state); err != nil || revision != 5 || state != "ready" {
		t.Fatal("unrelated published service was requeued", err)
	}
	if _, err := store.HandoverService(ctx, "source-service", input); err != nil {
		t.Fatal("lost-response retry failed", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_events WHERE kind='service.handover' AND event='succeeded'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("retry repeated transfer", err)
	}
}
