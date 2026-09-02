package center

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestPrivatePublicationVerificationUsesConfirmedGatewayAddress(t *testing.T) {
	var receivedHost string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		receivedHost = request.Host
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	serverAddress := strings.TrimPrefix(server.URL, "http://")
	gatewayAddress, port, err := net.SplitHostPort(serverAddress)
	if err != nil {
		t.Fatal(err)
	}
	client, closeClient := publicationVerificationHTTPClient(gatewayAddress)
	defer closeClient()
	response, err := client.Get("http://private-service.example.invalid:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent || receivedHost != "private-service.example.invalid:"+port {
		t.Fatalf("status=%d host=%q", response.StatusCode, receivedHost)
	}
}

func TestPublicationVerificationHTTPClientDoesNotFollowRedirects(t *testing.T) {
	var redirected atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer redirectTarget.Close()
	entry := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, redirectTarget.URL, http.StatusFound)
	}))
	defer entry.Close()

	entryAddress := strings.TrimPrefix(entry.URL, "http://")
	entryIP, port, err := net.SplitHostPort(entryAddress)
	if err != nil {
		t.Fatal(err)
	}
	client, closeClient := publicationVerificationHTTPClient(entryIP)
	defer closeClient()
	response, err := client.Get("http://entry.example.invalid:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("status=%d, want redirect response without following it", response.StatusCode)
	}
	if redirected.Load() != 0 {
		t.Fatal("publication verifier followed a redirect to a second target")
	}
}

func TestPublicPublicationVerificationAddressUsesOnlyExpectedPublicRecord(t *testing.T) {
	publication := PublicationView{
		Kind:      publicationPublic,
		DNSRecord: &DNSRecordInstruction{Value: "8.8.8.8"},
	}
	address, err := publicPublicationVerificationAddress(publication, []net.IPAddr{
		{IP: net.ParseIP("127.0.0.1")},
		{IP: net.ParseIP("8.8.8.8")},
	})
	if err != nil || address != "8.8.8.8" {
		t.Fatalf("address=%q err=%v", address, err)
	}

	publication.DNSRecord.Value = "10.0.0.8"
	if _, err := publicPublicationVerificationAddress(publication, []net.IPAddr{{IP: net.ParseIP("10.0.0.8")}}); err == nil || !strings.Contains(err.Error(), "not publicly routable") {
		t.Fatalf("private expected record was accepted: %v", err)
	}
	publication.DNSRecord.Value = "8.8.4.4"
	if _, err := publicPublicationVerificationAddress(publication, []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}); err == nil || !strings.Contains(err.Error(), "selected entry address") {
		t.Fatalf("mismatched DNS answer was accepted: %v", err)
	}
}

func TestCloudflarePublicationVerificationAddressSkipsPrivateDNSAnswers(t *testing.T) {
	publication := PublicationView{Kind: publicationCloudflare}
	address, err := publicPublicationVerificationAddress(publication, []net.IPAddr{
		{IP: net.ParseIP("127.0.0.1")},
		{IP: net.ParseIP("100.64.0.10")},
		{IP: net.ParseIP("1.1.1.1")},
	})
	if err != nil || address != "1.1.1.1" {
		t.Fatalf("address=%q err=%v", address, err)
	}
	if _, err := publicPublicationVerificationAddress(publication, []net.IPAddr{{IP: net.ParseIP("192.168.1.20")}}); err == nil || !strings.Contains(err.Error(), "publicly routable") {
		t.Fatalf("private-only Cloudflare DNS answer was accepted: %v", err)
	}
}

func TestPrivatePublicationVerificationTargetFailsClosed(t *testing.T) {
	publication := PublicationView{Kind: publicationHeadscale, DNSRecord: &DNSRecordInstruction{Value: "100.64.0.10"}}
	address, privateEntry, err := privatePublicationVerificationAddress(publication)
	if err != nil || !privateEntry || address != "100.64.0.10" {
		t.Fatalf("address=%q private=%t err=%v", address, privateEntry, err)
	}
	publication.DNSRecord = nil
	if _, privateEntry, err = privatePublicationVerificationAddress(publication); err == nil || !privateEntry {
		t.Fatalf("missing private entry address was accepted: private=%t err=%v", privateEntry, err)
	}
	if address, privateEntry, err = privatePublicationVerificationAddress(PublicationView{Kind: publicationPublic}); err != nil || privateEntry || address != "" {
		t.Fatalf("public publication unexpectedly received a private target: address=%q private=%t err=%v", address, privateEntry, err)
	}
}

func TestAutomaticPublicationVerificationIsBoundedAndMarksDegraded(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	store.publicationVerificationBackoff = func(int) time.Duration { return 0 }
	var attempts atomic.Int32
	store.verifyPublication = func(_ context.Context, id string, revision int64) (PublicationView, error) {
		attempts.Add(1)
		return PublicationView{ID: id, DesiredRevision: revision, Status: "pending", LastError: "DNS record has not propagated"}, nil
	}
	ctx := context.Background()
	seedVerificationPublication(t, store, publicationPublic, "manual", 1, 1, "applying")

	store.schedulePublicationVerification("verification-publication", 1)
	deadline := time.Now().Add(time.Second)
	for {
		var status, lastError string
		if err := store.db.QueryRowContext(ctx, `SELECT status, last_error FROM publications WHERE id = 'verification-publication'`).Scan(&status, &lastError); err != nil {
			t.Fatal(err)
		}
		if status == "degraded" {
			if attempts.Load() != publicationVerificationAttempts || lastError != "DNS record has not propagated" {
				t.Fatalf("attempts=%d status=%q lastError=%q", attempts.Load(), status, lastError)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("publication did not converge to degraded: attempts=%d status=%q lastError=%q", attempts.Load(), status, lastError)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPublicationVerificationRescheduleAtFinalAttemptIsNotLost(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	store.publicationVerificationBackoff = func(int) time.Duration { return 0 }
	lastAttempt := make(chan struct{})
	releaseLastAttempt := make(chan struct{})
	runAgain := make(chan struct{})
	var attempts atomic.Int32
	store.verifyPublication = func(_ context.Context, id string, revision int64) (PublicationView, error) {
		attempt := attempts.Add(1)
		if attempt == publicationVerificationAttempts {
			close(lastAttempt)
			<-releaseLastAttempt
		}
		status := "pending"
		if attempt > publicationVerificationAttempts {
			status = "ready"
			close(runAgain)
		}
		return PublicationView{ID: id, DesiredRevision: revision, Status: status, LastError: "not ready"}, nil
	}
	seedVerificationPublication(t, store, publicationPublic, "manual", 1, 1, "applying")

	store.schedulePublicationVerification("verification-publication", 1)
	select {
	case <-lastAttempt:
	case <-time.After(time.Second):
		close(releaseLastAttempt)
		t.Fatal("verification did not reach its final attempt")
	}
	store.schedulePublicationVerification("verification-publication", 1)
	close(releaseLastAttempt)
	select {
	case <-runAgain:
	case <-time.After(time.Second):
		t.Fatal("same-revision schedule at the end boundary was lost")
	}
	if attempts.Load() != publicationVerificationAttempts+1 {
		t.Fatalf("attempts=%d, want one new verification cycle", attempts.Load())
	}
}

func TestPublicationVerificationRevisionsRunSerially(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	store.publicationVerificationBackoff = func(int) time.Duration { return 0 }
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{})
	store.verifyPublication = func(_ context.Context, id string, revision int64) (PublicationView, error) {
		if revision == 1 {
			select {
			case <-firstStarted:
			default:
				close(firstStarted)
			}
			<-releaseFirst
			return PublicationView{ID: id, DesiredRevision: revision, Status: "pending"}, nil
		}
		close(secondStarted)
		return PublicationView{ID: id, DesiredRevision: revision, Status: "ready"}, nil
	}
	seedVerificationPublication(t, store, publicationPublic, "manual", 1, 1, "applying")

	store.schedulePublicationVerification("verification-publication", 1)
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		close(releaseFirst)
		t.Fatal("revision 1 verification did not start")
	}
	store.schedulePublicationVerification("verification-publication", 2)
	select {
	case <-secondStarted:
		close(releaseFirst)
		t.Fatal("revision 2 ran concurrently with revision 1")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("revision 2 did not run after revision 1 completed")
	}
}

func TestBackgroundPublicationVerificationCannotOverwriteANewerRevision(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	seedVerificationPublication(t, store, publicationPublic, "manual", 2, 0, "pending")
	ctx := context.Background()

	if _, err := store.markPublicationReady(ctx, "verification-publication", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.recordPublicationVerification(ctx, "verification-publication", 1, "stale failure"); err != nil {
		t.Fatal(err)
	}
	var desired, applied int64
	var status, lastError string
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision, applied_revision, status, last_error FROM publications WHERE id = 'verification-publication'`).Scan(&desired, &applied, &status, &lastError); err != nil {
		t.Fatal(err)
	}
	if desired != 2 || applied != 0 || status != "pending" || lastError != "" {
		t.Fatalf("stale verification changed revision 2: desired=%d applied=%d status=%q error=%q", desired, applied, status, lastError)
	}
	if _, err := store.markPublicationReady(ctx, "verification-publication", 2); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT applied_revision, status FROM publications WHERE id = 'verification-publication'`).Scan(&applied, &status); err != nil {
		t.Fatal(err)
	}
	if applied != 2 || status != "ready" {
		t.Fatalf("current verification did not become ready: applied=%d status=%q", applied, status)
	}
}

func TestInteractivePublicationVerificationCannotOverwriteANewerRevision(t *testing.T) {
	for _, responseStatus := range []int{http.StatusNoContent, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(responseStatus), func(t *testing.T) {
			requestStarted := make(chan struct{})
			releaseResponse := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				close(requestStarted)
				<-releaseResponse
				writer.WriteHeader(responseStatus)
			}))
			defer server.Close()

			store := openOrchestrationStore(t)
			defer store.Close()
			node := seedVerificationPublication(t, store, publicationLAN, "manual", 1, 0, "pending")
			ctx := context.Background()
			endpoint := strings.TrimPrefix(server.URL, "http://")
			_, port, err := net.SplitHostPort(endpoint)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE agent_network_profiles SET lan_address = '127.0.0.1' WHERE agent_id = ?`, node.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE services SET protocol = 'http', endpoint = ? WHERE id = 'verification-service'`, endpoint); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE publications SET hostname = ? WHERE id = 'verification-publication'`, "verification.example.test:"+port); err != nil {
				t.Fatal(err)
			}

			type verificationResult struct {
				publication PublicationView
				err         error
			}
			verified := make(chan verificationResult, 1)
			go func() {
				publication, err := store.VerifyPublication(context.Background(), "verification-publication")
				verified <- verificationResult{publication: publication, err: err}
			}()
			select {
			case <-requestStarted:
			case <-time.After(time.Second):
				t.Fatal("interactive verification did not reach the network check")
			}

			if _, err := store.db.ExecContext(ctx, `UPDATE publications SET desired_revision = 2, status = 'pending', last_error = 'new revision' WHERE id = 'verification-publication'`); err != nil {
				t.Fatal(err)
			}
			close(releaseResponse)
			result := <-verified
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.publication.DesiredRevision != 2 || result.publication.Status != "pending" || result.publication.LastError != "new revision" {
				t.Fatalf("verification returned stale state: %#v", result.publication)
			}
			var desired, applied int64
			var status, lastError string
			if err := store.db.QueryRowContext(ctx, `SELECT desired_revision, applied_revision, status, last_error FROM publications WHERE id = 'verification-publication'`).Scan(&desired, &applied, &status, &lastError); err != nil {
				t.Fatal(err)
			}
			if desired != 2 || applied != 0 || status != "pending" || lastError != "new revision" {
				t.Fatalf("stale interactive verification changed revision 2: desired=%d applied=%d status=%q error=%q", desired, applied, status, lastError)
			}
		})
	}
}

func TestTunnelCompletionWaitsForHTTPSVerification(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	node := seedVerificationPublication(t, store, publicationCloudflare, "cloudflare", 2, 0, "pending")
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	secretID, err := store.putSecret(ctx, tx, []byte("test-tunnel-token"), "cloudflare-tunnel:"+node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO cloudflare_tunnels(agent_id, tunnel_id, tunnel_name, token_secret_id, desired_revision, applied_revision, desired_json, status, attempt, created_at, updated_at)
		VALUES(?, 'test-tunnel-id', 'test-tunnel', ?, 2, 1, '{}', 'applying', 1, ?, ?)`, node.ID, secretID, now, now); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	store.verifyPublication = func(verifyCtx context.Context, id string, revision int64) (PublicationView, error) {
		startedOnce.Do(func() { close(started) })
		select {
		case <-verifyCtx.Done():
			return PublicationView{}, verifyCtx.Err()
		case <-release:
			return store.markPublicationReady(verifyCtx, id, revision)
		}
	}
	if err := store.completeTunnelState(ctx, node.ID, 2, 1, true, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Tunnel completion did not schedule publication verification")
	}
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM publications WHERE id = 'verification-publication'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "applying" {
		t.Fatalf("Tunnel publication was marked %q before HTTPS verification", status)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for status != "ready" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		if err := store.db.QueryRowContext(ctx, `SELECT status FROM publications WHERE id = 'verification-publication'`).Scan(&status); err != nil {
			t.Fatal(err)
		}
	}
	if status != "ready" {
		t.Fatalf("Tunnel publication did not become ready after HTTPS verification: %q", status)
	}
}

func TestTunnelCompletionAcknowledgesSupersededAgentOutboxResult(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	node := seedVerificationPublication(t, store, publicationCloudflare, "cloudflare", 2, 0, "pending")
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	secretID, err := store.putSecret(ctx, tx, []byte("test-tunnel-token"), "cloudflare-tunnel:"+node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO cloudflare_tunnels(agent_id, tunnel_id, tunnel_name, token_secret_id, desired_revision, applied_revision, desired_json, status, attempt, created_at, updated_at)
		VALUES(?, 'test-tunnel-id', 'test-tunnel', ?, 2, 0, '{}', 'applying', 2, ?, ?)`, node.ID, secretID, now, now); err != nil {
		t.Fatal(err)
	}

	if err := store.completeTunnelState(ctx, node.ID, 1, 1, true, ""); err != nil {
		t.Fatalf("superseded completion was not acknowledged: %v", err)
	}
	var desired, applied, attempt int64
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision, applied_revision, status, attempt FROM cloudflare_tunnels WHERE agent_id = ?`, node.ID).Scan(&desired, &applied, &status, &attempt); err != nil {
		t.Fatal(err)
	}
	if desired != 2 || applied != 0 || status != "applying" || attempt != 2 {
		t.Fatalf("superseded completion changed desired state: desired=%d applied=%d status=%q attempt=%d", desired, applied, status, attempt)
	}
}

func TestStoppedPublicationCompensatesInFlightCloudflareDNSCreation(t *testing.T) {
	createStarted := make(chan struct{})
	releaseCreate := make(chan struct{})
	deleted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /zones/zone/dns_records":
			_, _ = response.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
		case "POST /zones/zone/dns_records":
			close(createStarted)
			<-releaseCreate
			_, _ = response.Write([]byte(`{"success":true,"errors":[],"result":{"id":"created-after-stop"}}`))
		case "DELETE /zones/zone/dns_records/created-after-stop":
			deleted <- struct{}{}
			_, _ = response.Write([]byte(`{"success":true,"errors":[],"result":{}}`))
		default:
			t.Errorf("unexpected Cloudflare request: %s %s", request.Method, request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store := openOrchestrationStore(t)
	defer store.Close()
	node := seedVerificationPublication(t, store, publicationPublic, "cloudflare", 1, 0, "pending")
	store.cloudflareOAuth.APIURL = server.URL
	store.cloudflareOAuth.HTTPClient = server.Client()
	storeCloudflareOAuthIntegration(t, store, cloudflareOAuthToken{AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour)})

	type reconcileResult struct {
		ready bool
		err   error
	}
	reconciled := make(chan reconcileResult, 1)
	go func() {
		ready, err := store.reconcilePublicationDNS(context.Background(), "verification-publication", node.ID, "cloudflare", 1)
		reconciled <- reconcileResult{ready: ready, err: err}
	}()
	select {
	case <-createStarted:
	case <-time.After(time.Second):
		t.Fatal("Cloudflare DNS creation did not start")
	}

	stopped := make(chan error, 1)
	go func() { stopped <- store.StopPublication(context.Background(), "verification-publication") }()
	deadline := time.Now().Add(time.Second)
	for {
		var status string
		if err := store.db.QueryRowContext(context.Background(), `SELECT status FROM publications WHERE id = 'verification-publication'`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "stopped" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("publication stop did not commit while Cloudflare creation was in flight")
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseCreate)
	if result := <-reconciled; result.err != nil || result.ready {
		t.Fatalf("stale reconciliation result ready=%t err=%v", result.ready, result.err)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	select {
	case <-deleted:
	case <-time.After(time.Second):
		t.Fatal("Cloudflare DNS record created after stop was not compensated")
	}
	var status, recordID string
	if err := store.db.QueryRowContext(context.Background(), `SELECT status, dns_record_id FROM publications WHERE id = 'verification-publication'`).Scan(&status, &recordID); err != nil {
		t.Fatal(err)
	}
	if status != "stopped" || recordID != "" {
		t.Fatalf("stopped publication retained stale Cloudflare state: status=%q record=%q", status, recordID)
	}
}

func TestGatewayVerificationTargetsExcludeIndependentIngressOwners(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := seedVerificationPublication(t, store, publicationShared443, "manual", 3, 3, "ready")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO publications(
		id, service_id, kind, ingress_owner, entry_node_id, hostname, dns_provider,
		desired_revision, applied_revision, status, created_at, updated_at
	) VALUES
		('gateway-publication', 'verification-service', 'public_direct', 'site_gateway', ?, 'gateway.example.test', 'manual', 4, 4, 'ready', ?, ?),
		('tunnel-publication', 'verification-service', 'cloudflare_tunnel', 'tunnel_connector', ?, 'tunnel.example.test', 'cloudflare', 5, 5, 'ready', ?, ?)`, node.ID, now, now, node.ID, now, now); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	targets, err := store.publicationVerificationTargetsForGateway(ctx, tx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].id != "gateway-publication" || targets[0].revision != 4 {
		t.Fatalf("Gateway completion crossed ingress ownership boundaries: %#v", targets)
	}
}

func TestTunnelConnectorTargetsTheWebServiceWithoutSiteCaddy(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := seedVerificationPublication(t, store, publicationPublic, "manual", 1, 1, "ready")
	if _, err := store.db.ExecContext(ctx, `UPDATE services SET protocol = 'http', container_port = 8080, host_port = 8080, endpoint = '203.0.113.40:8080', app_protocol = 'http' WHERE id = 'verification-service'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE publications SET kind = 'cloudflare_tunnel', ingress_owner = 'tunnel_connector', dns_provider = 'cloudflare' WHERE id = 'verification-publication'`); err != nil {
		t.Fatal(err)
	}
	ingress, err := tunnelIngressForNode(ctx, store.db, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ingress) != 1 || ingress[0].Hostname != "verification.example.test" || ingress[0].Service != "http://203.0.113.40:8080" {
		t.Fatalf("Tunnel connector did not target the service directly: %#v", ingress)
	}
}

func seedVerificationPublication(t *testing.T, store *Store, kind, dnsProvider string, desiredRevision, appliedRevision int64, status string) AgentCredential {
	t.Helper()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "verification-node", NodeCapabilities{Docker: true, Tunnel: true}, []networking.Candidate{{Address: "10.0.0.40", Interface: "eth0", Kind: networking.KindLAN}, {Address: "203.0.113.40", Interface: "eth0", Kind: networking.KindPublic}}, networking.Profile{ServiceAddress: "10.0.0.40", LANAddress: "10.0.0.40", PublicAddress: "203.0.113.40", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id, name, node_id, site_id, app_key, status, created_at, updated_at)
		VALUES('verification-app', 'Verification', ?, ?, 'test/verification', 'running', ?, ?)`, node.ID, testSiteID(t, store), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, protocol, container_port, host_port, endpoint, source, app_protocol, observed_listen, status, created_at, updated_at)
		VALUES('verification-service', 'verification-app', ?, 'tcp', 'tcp', 443, 443, '203.0.113.40:443', 'observed', 'tcp', '203.0.113.40', 'ready', ?, ?)`, testSiteID(t, store), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO publications(id, service_id, kind, ingress_owner, entry_node_id, hostname, dns_provider, desired_revision, applied_revision, status, created_at, updated_at)
		VALUES('verification-publication', 'verification-service', ?, 'application_node', ?, 'verification.example.test', ?, ?, ?, ?, ?, ?)`, kind, node.ID, dnsProvider, desiredRevision, appliedRevision, status, now, now); err != nil {
		t.Fatal(err)
	}
	return node
}
