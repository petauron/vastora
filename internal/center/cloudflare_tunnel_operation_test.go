package center

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type cloudflareTunnelOperationAPI struct {
	mu               sync.Mutex
	createCalls      int
	deleteCalls      int
	disconnectCreate bool
	tokenFailures    int
	unrelatedSecret  bool
	name             string
	secret           string
	remote           bool
	beforeFirstToken func()
	tokenHookRan     bool
}

func (api *cloudflareTunnelOperationAPI) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	api.mu.Lock()
	defer api.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/accounts/account/cfd_tunnel":
		var input struct {
			Name   string `json:"name"`
			Secret string `json:"tunnel_secret"`
		}
		if json.NewDecoder(request.Body).Decode(&input) != nil || input.Name == "" || input.Secret == "" {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"success":false,"errors":[{"message":"invalid create"}]}`))
			return
		}
		api.createCalls++
		api.name = input.Name
		api.secret = input.Secret
		api.remote = true
		if api.disconnectCreate {
			api.disconnectCreate = false
			connection, _, err := writer.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			}
			return
		}
		_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{"id":"tunnel-owned","account_tag":"account","name":"` + api.name + `","config_src":"cloudflare"}}`))
	case request.Method == http.MethodGet && request.URL.Path == "/accounts/account/cfd_tunnel":
		result := `[]`
		if api.remote && request.URL.Query().Get("name") == api.name {
			result = `[{"id":"tunnel-owned","account_tag":"account","name":"` + api.name + `","config_src":"cloudflare"}]`
		}
		_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":` + result + `}`))
	case request.Method == http.MethodGet && request.URL.Path == "/accounts/account/cfd_tunnel/tunnel-owned/token":
		if !api.tokenHookRan && api.beforeFirstToken != nil {
			api.tokenHookRan = true
			api.beforeFirstToken()
		}
		if api.tokenFailures > 0 {
			api.tokenFailures--
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = writer.Write([]byte(`{"success":false,"errors":[{"message":"token unavailable"}]}`))
			return
		}
		secret := api.secret
		if api.unrelatedSecret {
			secret = base64.StdEncoding.EncodeToString([]byte("unrelated-owner-secret-material"))
		}
		claims, _ := json.Marshal(map[string]string{"a": "account", "t": "tunnel-owned", "s": secret})
		token := base64.RawURLEncoding.EncodeToString(claims)
		encodedToken, _ := json.Marshal(token)
		_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":` + string(encodedToken) + `}`))
	case request.Method == http.MethodDelete && request.URL.Path == "/accounts/account/cfd_tunnel/tunnel-owned":
		api.deleteCalls++
		api.remote = false
		_, _ = writer.Write([]byte(`{"success":true,"errors":[],"result":{}}`))
	default:
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"success":false,"errors":[{"message":"not found"}]}`))
	}
}

func openCloudflareTunnelOperationTestStore(t *testing.T, api *cloudflareTunnelOperationAPI) (*Store, string) {
	t.Helper()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.cloudflareOAuth.APIURL = server.URL
	store.cloudflareOAuth.HTTPClient = server.Client()
	storeCloudflareOAuthIntegration(t, store, cloudflareOAuthToken{AccessToken: "access", RefreshToken: "refresh", Scope: "zone.read dns.write argotunnel.write", ExpiresAt: time.Now().Add(time.Hour)})
	enrollment, err := store.CreateAgentEnrollment(context.Background(), AgentEnrollmentSpec{SiteID: testSiteID(t, store), Name: "Tunnel node", CenterURL: "https://center.example.com", Tunnel: true})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.EnrollAgent(context.Background(), enrollment.Token, "test", "linux", "amd64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	return store, agent.ID
}

func TestCloudflareTunnelCreationRecoversCommittedLostResponse(t *testing.T) {
	api := &cloudflareTunnelOperationAPI{disconnectCreate: true}
	store, agentID := openCloudflareTunnelOperationTestStore(t, api)
	if err := store.ensureCloudflareTunnel(context.Background(), agentID); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	createCalls := api.createCalls
	api.mu.Unlock()
	if createCalls != 1 {
		t.Fatalf("Cloudflare Tunnel creates = %d, want 1", createCalls)
	}
	var tunnelID string
	if err := store.db.QueryRow(`SELECT tunnel_id FROM cloudflare_tunnels WHERE agent_id = ?`, agentID).Scan(&tunnelID); err != nil || tunnelID != "tunnel-owned" {
		t.Fatalf("recovered Tunnel = %q, err=%v", tunnelID, err)
	}
	if _, exists, err := store.readCloudflareTunnelOperation(context.Background(), agentID); err != nil || exists {
		t.Fatalf("completed operation remains: exists=%v err=%v", exists, err)
	}
}

func TestCloudflareTunnelCreationResumesTokenAndDatabaseFailures(t *testing.T) {
	api := &cloudflareTunnelOperationAPI{tokenFailures: 1}
	store, agentID := openCloudflareTunnelOperationTestStore(t, api)
	if err := store.ensureCloudflareTunnel(context.Background(), agentID); err == nil || !strings.Contains(err.Error(), "token retrieval") {
		t.Fatalf("token failure was not preserved: %v", err)
	}
	operation, exists, err := store.readCloudflareTunnelOperation(context.Background(), agentID)
	if err != nil || !exists || operation.Phase != "created" || operation.TunnelID != "tunnel-owned" {
		t.Fatalf("token failure was not resumable: operation=%#v exists=%v err=%v", operation, exists, err)
	}
	dataDir, cloudflareConfig := store.dataDir, store.cloudflareOAuth
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.cloudflareOAuth = cloudflareConfig
	if _, err := store.db.Exec(`CREATE TRIGGER fail_cloudflare_tunnel_secret BEFORE INSERT ON secrets BEGIN SELECT RAISE(ABORT, 'injected Tunnel secret failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.ensureCloudflareTunnel(context.Background(), agentID); err == nil || !strings.Contains(err.Error(), "injected Tunnel secret failure") {
		t.Fatalf("secret failure was not reported after restart: %v", err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_cloudflare_tunnel_secret`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_cloudflare_tunnel_insert BEFORE INSERT ON cloudflare_tunnels BEGIN SELECT RAISE(ABORT, 'injected Tunnel insert failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.ensureCloudflareTunnel(context.Background(), agentID); err == nil || !strings.Contains(err.Error(), "injected Tunnel insert failure") {
		t.Fatalf("database failure was not reported: %v", err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_cloudflare_tunnel_insert`); err != nil {
		t.Fatal(err)
	}
	if err := store.ensureCloudflareTunnel(context.Background(), agentID); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	createCalls := api.createCalls
	api.mu.Unlock()
	if createCalls != 1 {
		t.Fatalf("resumed operation created %d Tunnels", createCalls)
	}
}

func TestCloudflareTunnelCreationNeverAdoptsSameNameWithoutOwnershipProof(t *testing.T) {
	api := &cloudflareTunnelOperationAPI{disconnectCreate: true, unrelatedSecret: true}
	store, agentID := openCloudflareTunnelOperationTestStore(t, api)
	err := store.ensureCloudflareTunnel(context.Background(), agentID)
	if err == nil || !strings.Contains(err.Error(), "none prove ownership") {
		t.Fatalf("unrelated Tunnel was not rejected: %v", err)
	}
	err = store.ensureCloudflareTunnel(context.Background(), agentID)
	if err == nil || !strings.Contains(err.Error(), "none prove ownership") {
		t.Fatalf("retry adopted an unrelated Tunnel: %v", err)
	}
	api.mu.Lock()
	createCalls, deleteCalls := api.createCalls, api.deleteCalls
	api.mu.Unlock()
	if createCalls != 1 || deleteCalls != 0 {
		t.Fatalf("unsafe same-name handling: creates=%d deletes=%d", createCalls, deleteCalls)
	}
	diagnostics, err := store.Diagnostics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics.PendingOperations) != 1 || diagnostics.PendingOperations[0].ResourceID != agentID || diagnostics.PendingOperations[0].LastError == "" {
		t.Fatalf("pending operation was not exposed safely: %#v", diagnostics.PendingOperations)
	}
}

func TestCloudflareTunnelCreationConcurrentCallsConverge(t *testing.T) {
	api := &cloudflareTunnelOperationAPI{}
	store, agentID := openCloudflareTunnelOperationTestStore(t, api)
	errorsChannel := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			ready.Wait()
			errorsChannel <- store.ensureCloudflareTunnel(context.Background(), agentID)
		}()
	}
	for range 2 {
		if err := <-errorsChannel; err != nil {
			t.Fatal(err)
		}
	}
	api.mu.Lock()
	createCalls := api.createCalls
	api.mu.Unlock()
	if createCalls != 1 {
		t.Fatalf("concurrent requests created %d Tunnels", createCalls)
	}
}
