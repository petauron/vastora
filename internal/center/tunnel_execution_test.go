package center

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
)

func TestTunnelOriginSyncPreservesSettingsAndVerifiesReadBack(t *testing.T) {
	for _, outcome := range []string{"confirmed", "write-rejected", "read-back-stale"} {
		t.Run(outcome, func(t *testing.T) {
			config := map[string]any{"originRequest": map[string]any{"connectTimeout": 10}, "ingress": []any{
				map[string]any{"hostname": "sub.example.com", "service": "http://native:2096", "originRequest": map[string]any{"httpHostHeader": "sub.example.com"}},
				map[string]any{"hostname": "panel.example.com", "service": "http://native:2053"}, map[string]any{"service": "http_status:404"},
			}}
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/accounts/account/cfd_tunnel/tunnel/configurations" || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("unexpected configuration scope or authentication")
				}
				if r.Method == http.MethodPut {
					writes++
					if outcome == "write-rejected" {
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = w.Write([]byte(`{"success":false}`))
						return
					}
					var input struct {
						Config map[string]any `json:"config"`
					}
					if json.NewDecoder(r.Body).Decode(&input) != nil {
						t.Error("invalid configuration update")
					}
					if outcome == "confirmed" {
						config = input.Config
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"source": "cloudflare", "config": config}})
			}))
			defer server.Close()
			client := cloudflareClient{accountID: "account", token: "test-token", baseURL: server.URL, http: server.Client()}
			wanted := []TunnelTaskIngress{{Hostname: "sub.example.com", Service: "http://100.64.0.2:2097"}, {Hostname: "panel.example.com", Service: "http://native:2053"}}
			err := client.syncTunnelConfiguration(context.Background(), "tunnel", wanted)
			if (err == nil) != (outcome == "confirmed") || writes != 1 {
				t.Fatal("unconfirmed origin accepted or mutation replayed", err, writes)
			}
			if outcome == "confirmed" {
				if config["originRequest"] == nil || config["ingress"].([]any)[0].(map[string]any)["originRequest"] == nil {
					t.Fatal("unrelated origin settings were lost")
				}
				if err := client.syncTunnelConfiguration(context.Background(), "tunnel", wanted); err != nil || writes != 1 {
					t.Fatal("matching remote state was unnecessarily written", err, writes)
				}
			}
		})
	}
}

func TestTunnelExecutionConsumesOfferBeforeCloudflareAndFencesFailure(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "tunnel-origin", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.16", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.16", LANAddress: "10.0.0.16", EnabledKinds: []string{networking.KindLAN}})
	session := "tunnel-execution-test-session"
	if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	storeCloudflareOAuthIntegration(t, store, cloudflareOAuthToken{AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour)})
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO cloudflare_tunnels(agent_id,tunnel_id,tunnel_name,token_secret_id,desired_revision,applied_revision,status,attempt,created_at,updated_at)
		SELECT ?,'tunnel','test',secret_id,2,1,'applying',1,?,? FROM network_integrations WHERE kind='cloudflare'`, node.ID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	task := AgentTask{ID: tunnelTaskID(node.ID, 2), Kind: "tunnel.state.apply", Attempt: 1, Revision: 2, TunnelState: &TunnelTaskState{Revision: 2, Status: "running", Ingress: []TunnelTaskIngress{{Hostname: "sub.example.com", Service: "http://100.64.0.2:2097"}}}}
	auth, err := store.PersistExecutionAuthorization(ctx, node.ID, session, task)
	if err != nil {
		t.Fatal(err)
	}
	methods := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		var state string
		if err := store.db.QueryRow(`SELECT state FROM task_executions WHERE id=?`, auth.ID).Scan(&state); err != nil || state != "running" {
			t.Error("external API called before one-use offer consumption", state, err)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"success":false}`))
	}))
	defer server.Close()
	store.cloudflareOAuth.APIURL, store.cloudflareOAuth.HTTPClient = server.URL, server.Client()
	if store.StartExecution(ctx, node.ID, session, auth.ID, auth.Digest) == nil {
		t.Fatal("provider error was accepted")
	}
	if err := store.StartExecution(ctx, node.ID, session, auth.ID, auth.Digest); !errors.Is(err, errExecutionAuthorization) {
		t.Fatal("offer replay was accepted", err)
	}
	var state string
	if err := store.db.QueryRow(`SELECT state FROM task_executions WHERE id=?`, auth.ID).Scan(&state); err != nil || state != "unknown" || !reflect.DeepEqual(methods, []string{http.MethodGet}) {
		t.Fatal("failure lost its fence or repeated the external operation", state, methods, err)
	}
}
