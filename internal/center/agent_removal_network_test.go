package center

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/networking"
)

func serveRemovalHeadscale(t *testing.T, s *Store, handler http.Handler) {
	t.Helper()
	headscale := httptest.NewTLSServer(handler)
	t.Cleanup(headscale.Close)
	endpoint := "https://example.com:" + strings.Split(strings.TrimPrefix(headscale.URL, "https://"), ":")[1]
	s.headscaleHTTPClient = headscale.Client()
	s.headscaleAllowedEndpoints = []string{endpoint}
	s.builtinHeadscaleDialAddress = strings.TrimPrefix(headscale.URL, "https://")
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	secretID, err := s.putSecret(ctx, tx, []byte("test-api-key"), "integration:headscale")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO network_integrations(kind,mode,endpoint,secret_id,status,created_at,updated_at) VALUES('headscale','builtin',?,?,'configured','','')`, endpoint, secretID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveOfflineAgentPrivateIdentityRejectsUnownedNodes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		response    string
		observation string
		wantError   string
	}{
		{
			name:      "enrolling user does not replace the ownership tag",
			response:  `{"nodes":[{"id":"10","nodeKey":"nodekey:old-key","ipAddresses":["100.64.0.86"],"user":{"name":"vastora"},"tags":["tag:other-service"]}]}`,
			wantError: "private node is not owned by Vastora",
		},
		{
			name:      "gateway tag alone is insufficient",
			response:  `{"nodes":[{"id":"10","nodeKey":"nodekey:old-key","ipAddresses":["100.64.0.86"],"user":{"name":"tagged-devices"},"tags":["tag:vastora-gateway"]}]}`,
			wantError: "private node is not owned by Vastora",
		},
		{
			name:      "removed API fields cannot authorize deletion",
			response:  `{"nodes":[{"id":"10","nodeKey":"nodekey:old-key","ipAddresses":["100.64.0.86"],"user":{"name":"vastora"},"forcedTags":["tag:vastora-agent"],"validTags":["tag:vastora-agent"]}]}`,
			wantError: "private node is not owned by Vastora",
		},
		{
			name:      "tag without node key is insufficient",
			response:  `{"nodes":[{"id":"10","ipAddresses":["100.64.0.86"],"user":{"name":"tagged-devices"},"tags":["tag:vastora-agent"]}]}`,
			wantError: "private node is not owned by Vastora",
		},
		{
			name:        "same address with another authenticated key is rejected",
			response:    `{"nodes":[{"id":"10","nodeKey":"nodekey:replacement-key","ipAddresses":["100.64.0.86"],"user":{"name":"tagged-devices"},"tags":["tag:vastora-agent"]}]}`,
			observation: `{"address":"100.64.0.86","publicKey":"nodekey:old-key"}`,
			wantError:   "private node no longer matches its authenticated observation",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openOrchestrationStore(t)
			defer s.Close()
			ctx := context.Background()
			node := enrollOrchestrationNode(t, s, "private-expired", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "100.64.0.86", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.86", HeadscaleAddress: "100.64.0.86", EnabledKinds: []string{networking.KindHeadscale}})
			if _, err := s.db.Exec(`UPDATE agents SET tailscale_ownership='managed' WHERE id=?`, node.ID); err != nil {
				t.Fatal(err)
			}
			if tc.observation != "" {
				if _, err := s.db.Exec(`INSERT INTO landing_client_capabilities(node_id,generation,peer_json,observed_at) VALUES(?,1,?,'')`, node.ID, tc.observation); err != nil {
					t.Fatal(err)
				}
			}
			listed := false
			serveRemovalHeadscale(t, s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/node" {
					t.Errorf("unowned private identity touched: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				listed = true
				_, _ = w.Write([]byte(tc.response))
			}))
			expireRemovalNode(t, s, node.ID)
			if err := s.StartAgentRemoval(ctx, node.ID, "private-expired"); err != nil {
				t.Fatal(err)
			}
			if err := s.resumeAgentRemovals(ctx); err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("unexpected ownership result: %v", err)
			}
			if !listed {
				t.Fatal("private identity was not checked")
			}
			if removalCount(t, s, `SELECT COUNT(*) FROM agent_removals WHERE agent_id=? AND state='failed' AND prepared=0 AND headscale_done=0 AND headscale_identity_json='{}'`, node.ID) != 1 {
				t.Fatal("unsafe identity was saved or cleanup advanced")
			}
			if removalCount(t, s, `SELECT COUNT(*) FROM agents WHERE id=?`, node.ID) != 1 {
				t.Fatal("failed cleanup purged the node")
			}
		})
	}
}
