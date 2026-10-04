package center

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func setReinstallPrivateObservation(t *testing.T, s *Store, agentID string) {
	t.Helper()
	if _, err := s.db.Exec(`UPDATE agents SET tailscale_ownership='managed' WHERE id=?`, agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO agent_private_peer_capabilities(node_id,generation,peer_json,observed_at) VALUES(?,1,'{"address":"100.64.0.2","publicKey":"nodekey:old-private-key"}','2026-01-01T00:00:00Z')`, agentID); err != nil {
		t.Fatal(err)
	}
}

func TestAgentReinstallWithdrawsPrivateIdentityBeforeBootstrap(t *testing.T) {
	s, node := prepareReinstallNode(t)
	setReinstallPrivateObservation(t, s, node.ID)
	if _, err := s.db.Exec(`UPDATE settings SET value='headscale' WHERE key=?`, agentConnectionModeSetting); err != nil {
		t.Fatal(err)
	}
	old := removalHeadscaleNode{ID: "42", NodeKey: "nodekey:old-private-key", IPAddresses: []string{"100.64.0.2"}, Tags: []string{"tag:vastora-agent"}}
	other := removalHeadscaleNode{ID: "43", NodeKey: "nodekey:other", IPAddresses: []string{"100.64.0.3"}, Tags: []string{"tag:vastora-agent"}}
	deleted, verified := false, false
	deletes, keys := 0, 0
	serveRemovalHeadscale(t, s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/node":
			nodes := []removalHeadscaleNode{other}
			if !deleted {
				nodes = append(nodes, old)
			} else {
				verified = true
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": nodes})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/node/42":
			var saved reinstallPrivateIdentity
			var encoded []byte
			if err := s.db.QueryRow(`SELECT private_identity_json FROM agent_reinstall_operations WHERE agent_id=?`, node.ID).Scan(&encoded); err != nil || json.Unmarshal(encoded, &saved) != nil || saved.ID != "42" || saved.NodeKey != old.NodeKey || saved.Address != "100.64.0.2" || saved.Endpoint == "" {
				t.Errorf("identity not durably recorded before withdrawal: %+v %v", saved, err)
			}
			if err := s.authenticateAgent(context.Background(), node.ID, node.Credential); err == nil {
				t.Error("old management credential still valid during withdrawal")
			}
			deleted = true
			deletes++
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/api/v1/user" && r.Method == http.MethodGet:
			if !verified {
				t.Error("bootstrap began before withdrawal verification")
			}
			_, _ = w.Write([]byte(`{"users":[{"id":"1","name":"vastora"}]}`))
		case r.URL.Path == "/api/v1/preauthkey" && r.Method == http.MethodPost:
			keys++
			_, _ = w.Write([]byte(`{"key":"new-one-time-bootstrap"}`))
		default:
			t.Errorf("unexpected private controller mutation: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	ctx := context.Background()
	input := reviewedReconnectInput(t, s, node.ID)
	enrollment, err := s.CreateAgentReconnectEnrollment(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateAgentReconnectEnrollment(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || enrollment != replay || deletes != 1 || keys != 1 || !verified {
		t.Fatalf("identity/bootstrap replayed: deletes=%d keys=%d verified=%v err=%v", deletes, keys, verified, err)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil || plan.Recovery.PrivateIsolation != "withdrawn" || plan.Recovery.State != "awaiting_enrollment" {
		t.Fatalf("isolation progress missing: %+v %v", plan.Recovery, err)
	}
}

func TestAgentReinstallPrivateIdentityFailureStopsAndPersists(t *testing.T) {
	for _, scenario := range []string{"missing_evidence", "external", "address_reassigned", "key_moved", "wrong_owner", "ambiguous", "delete_failed", "verification_failed", "still_present", "already_absent", "missing_list"} {
		t.Run(scenario, func(t *testing.T) {
			s, node := prepareReinstallNode(t)
			setReinstallPrivateObservation(t, s, node.ID)
			if scenario == "missing_evidence" {
				if _, err := s.db.Exec(`DELETE FROM agent_private_peer_capabilities WHERE node_id=?`, node.ID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "external" {
				if _, err := s.db.Exec(`UPDATE agents SET tailscale_ownership='external' WHERE id=?`, node.ID); err != nil {
					t.Fatal(err)
				}
			}
			deletes, requests := 0, 0
			serveRemovalHeadscale(t, s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if scenario == "missing_list" {
					_, _ = w.Write([]byte(`{}`))
					return
				}
				if r.Method == http.MethodDelete && r.URL.Path == "/api/v1/node/42" {
					deletes++
					if scenario == "delete_failed" {
						// The response is uncertain, including whether DELETE applied.
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					_, _ = w.Write([]byte(`{}`))
					return
				}
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/node" {
					t.Errorf("unsafe bootstrap or identity mutation: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if scenario == "verification_failed" && deletes != 0 {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				old := removalHeadscaleNode{ID: "42", NodeKey: "nodekey:old-private-key", IPAddresses: []string{"100.64.0.2"}, Tags: []string{"tag:vastora-agent"}}
				switch scenario {
				case "address_reassigned":
					old.NodeKey = "nodekey:another-machine"
				case "key_moved":
					old.IPAddresses = []string{"100.64.0.9"}
				case "wrong_owner":
					old.Tags = []string{"tag:unrelated"}
				}
				nodes := []removalHeadscaleNode{old}
				if scenario == "ambiguous" {
					duplicate := old
					duplicate.ID = "43"
					nodes = append(nodes, duplicate)
				} else if scenario == "already_absent" {
					nodes = []removalHeadscaleNode{}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"nodes": nodes})
			}))
			ctx := context.Background()
			input := reviewedReconnectInput(t, s, node.ID)
			_, err := s.CreateAgentReconnectEnrollment(ctx, node.ID, "reinstall-review-admin", input)
			if scenario == "already_absent" {
				if err != nil || deletes != 0 {
					t.Fatalf("verified absent identity cannot proceed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe private identity produced a command")
			}
			wantDeletes := 0
			if scenario == "delete_failed" || scenario == "verification_failed" || scenario == "still_present" {
				wantDeletes = 1
			}
			if deletes != wantDeletes {
				t.Fatalf("unsafe delete count: %d want %d", deletes, wantDeletes)
			}
			before := requests
			if _, err = s.CreateAgentReconnectEnrollment(ctx, node.ID, "reinstall-review-admin", input); err == nil || requests != before {
				t.Fatalf("failed withdrawal replayed: %v", err)
			}
			var grants int
			if err = s.db.QueryRow(`SELECT COUNT(*) FROM agent_enrollment_tokens WHERE target_agent_id=?`, node.ID).Scan(&grants); err != nil || grants != 0 {
				t.Fatalf("grant created despite missing isolation: %d %v", grants, err)
			}
			var path, name string
			var seq int
			if err = s.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
				t.Fatal(err)
			}
			s.Close()
			reopened, err := Open(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			plan, err := reopened.AgentReinstallPlan(ctx, node.ID)
			if err != nil || plan.Recovery == nil || plan.Recovery.State != "failed" || plan.Recovery.PrivateIsolation != "pending" || plan.Recovery.LastError == "" {
				t.Fatalf("failure/evidence not durable: %+v %v", plan.Recovery, err)
			}
			if wantDeletes != 0 {
				var encoded string
				if err = reopened.db.QueryRow(`SELECT private_identity_json FROM agent_reinstall_operations WHERE id=?`, input.OperationID).Scan(&encoded); err != nil || !strings.Contains(encoded, `"id":"42"`) {
					t.Fatalf("uncertain external side effect lost exact identity: %s %v", encoded, err)
				}
			}
		})
	}
}

func TestAgentReinstallExplicitContinuationUsesSavedIdentityAndAttempt(t *testing.T) {
	s, node := prepareReinstallNode(t)
	setReinstallPrivateObservation(t, s, node.ID)
	deleted, conflict := false, false
	deletes, requests := 0, 0
	serveRemovalHeadscale(t, s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method == http.MethodDelete && r.URL.Path == "/api/v1/node/42" {
			deletes++
			deleted = true
			w.WriteHeader(http.StatusBadGateway) // applied, response lost
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/node" {
			t.Errorf("unexpected mutation: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		nodes := []removalHeadscaleNode{}
		if !deleted {
			nodes = append(nodes, removalHeadscaleNode{ID: "42", NodeKey: "nodekey:old-private-key", IPAddresses: []string{"100.64.0.2"}, Tags: []string{"tag:vastora-agent"}})
		}
		if conflict {
			nodes = append(nodes, removalHeadscaleNode{ID: "43", NodeKey: "nodekey:unrelated-machine", IPAddresses: []string{"100.64.0.2"}, Tags: []string{"tag:vastora-agent"}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"nodes": nodes})
	}))
	ctx := context.Background()
	input := reviewedReconnectInput(t, s, node.ID)
	if _, err := s.CreateAgentReconnectEnrollment(ctx, node.ID, "reinstall-review-admin", input); err == nil || deletes != 1 {
		t.Fatalf("uncertain deletion should stop: %v", err)
	}
	continuation := AgentReinstallContinueInput{OperationID: input.OperationID, ExpectedAttempt: 1, ConfirmIsolation: true}
	before := requests
	if _, err := s.ContinueAgentReinstallIsolation(ctx, node.ID, "another-admin", continuation); err == nil || requests != before {
		t.Fatalf("another authorization continued the operation: %v", err)
	}
	conflict = true
	if _, err := s.ContinueAgentReinstallIsolation(ctx, node.ID, "reinstall-review-admin", continuation); err == nil || deletes != 1 {
		t.Fatalf("new address owner was substituted for old identity: %v", err)
	}
	before = requests
	if _, err := s.ContinueAgentReinstallIsolation(ctx, node.ID, "reinstall-review-admin", continuation); err == nil || requests != before {
		t.Fatalf("duplicate continuation repeated an attempt: %v", err)
	}
	conflict = false
	continuation.ExpectedAttempt = 2
	enrollment, err := s.ContinueAgentReinstallIsolation(ctx, node.ID, "reinstall-review-admin", continuation)
	if err != nil || enrollment.Token == "" || deletes != 1 {
		t.Fatalf("verified absence should resume without replaying DELETE: %v", err)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil || plan.Recovery.Attempt != 3 || plan.Recovery.PrivateIsolation != "withdrawn" {
		t.Fatalf("continuation not persisted: %+v %v", plan.Recovery, err)
	}
	before = requests
	continuation.ExpectedAttempt = 3
	if _, err := s.ContinueAgentReinstallIsolation(ctx, node.ID, "reinstall-review-admin", continuation); err == nil || requests != before {
		t.Fatalf("network continuation replayed command preparation: %v", err)
	}
}
