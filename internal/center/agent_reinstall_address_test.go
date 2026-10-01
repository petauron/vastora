package center

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
)

func replacementNetworkFixture(t *testing.T) (*Store, AgentCredential, NodeHeartbeat) {
	t.Helper()
	s, node := prepareReinstallNode(t)
	if _, err := s.db.Exec(`INSERT INTO agent_network_profiles(agent_id,service_address,lan_address,enabled_kinds_json,direct_public,confirmed_at,candidate_observed_at) VALUES(?,'10.0.0.7','10.0.0.7','["lan"]',0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO agent_network_candidates(agent_id,address,interface_name,kind,observed_at) VALUES(?,'10.0.0.7','eth0','lan','2026-01-01T00:00:00Z')`, node.ID); err != nil {
		t.Fatal(err)
	}
	enrollment, err := createReviewedReconnect(t, s, context.Background(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	key := testAgentPublicKey(t)
	replacement, err := s.EnrollAgent(context.Background(), enrollment.Token, Version, "linux", "amd64", key)
	if err != nil {
		t.Fatal(err)
	}
	heartbeat := NodeHeartbeat{Version: Version, PublicKey: key, Capabilities: NodeCapabilities{Docker: true}, Roles: []string{"worker"}, NetworkCandidates: []networking.Candidate{{Address: "10.0.0.8", Interface: "eth0", Kind: "lan"}}}
	return s, replacement, heartbeat
}

func networkApprovalInput(t *testing.T, s *Store, node string) AgentReinstallNetworkInput {
	t.Helper()
	plan, err := s.AgentReinstallPlan(context.Background(), node)
	if err != nil {
		t.Fatal(err)
	}
	return AgentReinstallNetworkInput{OperationID: plan.Recovery.ID, PlanRevision: plan.Revision, ConfirmMigration: true, Profile: networking.Profile{ServiceAddress: "10.0.0.8", LANAddress: "10.0.0.8", EnabledKinds: []string{"lan"}}}
}

func TestAgentReinstallAddressApprovalPreservesFenceAndSurvivesRestart(t *testing.T) {
	s, node, heartbeat := replacementNetworkFixture(t)
	ctx := context.Background()
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil || plan.NetworkReview == nil || plan.NetworkReview.Ready || len(plan.NetworkReview.Candidates) != 0 {
		t.Fatalf("enrollment inherited old address evidence: %+v %v", plan.NetworkReview, err)
	}
	if _, err = s.ApproveAgentReinstallNetwork(ctx, node.ID, "reinstall-review-admin", networkApprovalInput(t, s, node.ID)); err == nil {
		t.Fatal("enrollment without fresh heartbeat approved")
	}
	if err = s.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	input := networkApprovalInput(t, s, node.ID)
	// An unchanged heartbeat updates times, not the meaning of the review.
	oldNow := s.now
	s.now = func() time.Time { return oldNow().Add(time.Second) }
	if err = s.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	approved, err := s.ApproveAgentReinstallNetwork(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Previous == nil || approved.Previous.ServiceAddress != "10.0.0.7" || approved.Profile.ServiceAddress != "10.0.0.8" || approved.AuthorizedBy != "reinstall-review-admin" {
		t.Fatalf("missing approval evidence: %+v", approved)
	}
	var profiles, retained, events int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM agent_network_profiles WHERE agent_id=?`, node.ID).Scan(&profiles); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM agent_network_profile_recovery WHERE agent_id=?`, node.ID).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if profiles != 0 || retained != 1 {
		t.Fatalf("approval activated a bare host or discarded old binding: %d %d", profiles, retained)
	}
	if _, err = s.ClaimNextTask(ctx, node.ID, node.Credential); !errors.Is(err, errExecutionBlocked) {
		t.Fatalf("network approval released work: %v", err)
	}
	var path, name string
	var seq int
	if err = s.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replay, err := reopened.ApproveAgentReinstallNetwork(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || !reflect.DeepEqual(approved, replay) {
		t.Fatalf("lost approval response did not recover exactly: %+v %v", replay, err)
	}
	if err = reopened.db.QueryRow(`SELECT COUNT(*) FROM task_events WHERE kind='agent.reinstall.network'`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("approval repeated: %d %v", events, err)
	}
}

func TestAgentReinstallAddressReapprovalRetainsEveryDecision(t *testing.T) {
	s, node, h := replacementNetworkFixture(t)
	ctx := context.Background()
	if err := s.RecordAgentHeartbeat(ctx, node.ID, node.Credential, h); err != nil {
		t.Fatal(err)
	}
	input := networkApprovalInput(t, s, node.ID)
	first, err := s.ApproveAgentReinstallNetwork(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil || !plan.NetworkReview.ApprovalCurrent {
		t.Fatalf("approved current observation missing: %+v %v", plan.NetworkReview, err)
	}
	h.NetworkCandidates[0].Address = "10.0.0.9"
	if err = s.RecordAgentHeartbeat(ctx, node.ID, node.Credential, h); err != nil {
		t.Fatal(err)
	}
	plan, err = s.AgentReinstallPlan(ctx, node.ID)
	if err != nil || plan.NetworkReview.ApprovalCurrent {
		t.Fatalf("changed observation kept approval current: %+v %v", plan.NetworkReview, err)
	}
	next := networkApprovalInput(t, s, node.ID)
	next.Profile.ServiceAddress = "10.0.0.9"
	next.Profile.LANAddress = "10.0.0.9"
	if _, err = s.ApproveAgentReinstallNetwork(ctx, node.ID, "reinstall-review-admin", next); err != nil {
		t.Fatal(err)
	}
	var encoded []byte
	var count int
	if err = s.db.QueryRow(`SELECT approval_json FROM agent_reinstall_network_approvals WHERE operation_id=? AND plan_revision=?`, input.OperationID, input.PlanRevision).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var historical AgentReinstallNetworkApproval
	if json.Unmarshal(encoded, &historical) != nil || !reflect.DeepEqual(*first, historical) {
		t.Fatal("reapproval overwrote earlier authorization")
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM agent_reinstall_network_approvals`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("missing history: %d %v", count, err)
	}
}

func TestAgentReinstallAddressApprovalRejectsChangedOrUntrustedEvidence(t *testing.T) {
	for _, scenario := range []string{"unconfirmed", "wrong_admin", "wrong_operation", "unknown_address", "disabled_network", "stale_heartbeat", "changed_candidate", "stale_public_mapping", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			s, node, h := replacementNetworkFixture(t)
			ctx := context.Background()
			if err := s.RecordAgentHeartbeat(ctx, node.ID, node.Credential, h); err != nil {
				t.Fatal(err)
			}
			input := networkApprovalInput(t, s, node.ID)
			admin := "reinstall-review-admin"
			switch scenario {
			case "unconfirmed":
				input.ConfirmMigration = false
			case "wrong_admin":
				admin = "another-admin"
			case "wrong_operation":
				input.OperationID = "different-operation"
			case "unknown_address":
				input.Profile.ServiceAddress = "10.0.0.99"
			case "disabled_network":
				input.Profile = networking.Profile{ServiceAddress: "127.0.0.1"}
			case "stale_heartbeat":
				now := s.now()
				s.now = func() time.Time { return now.Add(agentConnectedMaxAge + time.Second) }
			case "changed_candidate":
				h.NetworkCandidates[0].Address = "10.0.0.9"
				if err := s.RecordAgentHeartbeat(ctx, node.ID, node.Credential, h); err != nil {
					t.Fatal(err)
				}
			case "stale_public_mapping":
				input.Profile.DirectPublic = true
				input.Profile.EnabledKinds = append(input.Profile.EnabledKinds, "public")
				input.Profile.PublicAddress = "203.0.113.7"
				input.Profile.PublicBindAddress = "10.0.0.8"
				input.Profile.PublicMode = "nat"
			case "revoked":
				if _, err := s.db.Exec(`UPDATE agents SET credential_revoked_at='revoked' WHERE id=?`, node.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.ApproveAgentReinstallNetwork(ctx, node.ID, admin, input); err == nil {
				t.Fatal("unsafe approval accepted")
			}
			var count int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM agent_reinstall_network_approvals`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed approval changed history: %d %v", count, err)
			}
		})
	}
}

func TestAgentReinstallAddressApprovalChecksNewPrivateIdentity(t *testing.T) {
	for _, scenario := range []string{"valid", "same_ip_new_key", "old_key", "wrong_owner", "missing_peer", "wrong_address", "ambiguous", "missing_list", "controller_failed", "changed_during_inspection", "controller_changed", "old_identity_present"} {
		t.Run(scenario, func(t *testing.T) {
			s, node, h := replacementNetworkFixture(t)
			ctx := context.Background()
			key, address := "nodekey:replacement-key", "100.64.0.8"
			if scenario == "same_ip_new_key" {
				address = "100.64.0.2"
			}
			h.TailscaleOwnership = "managed"
			h.NetworkCandidates = append(h.NetworkCandidates, networking.Candidate{Address: address, Kind: "headscale", Interface: "tailscale0"})
			h.LandingClientRuntime = &landing.ClientRuntime{Generation: landing.ClientRuntimeGeneration, Peer: landing.PeerIdentity{ID: "new-peer", PublicKey: key, Address: address}}
			if scenario == "old_key" {
				h.LandingClientRuntime.Peer.PublicKey = "nodekey:old-key"
			}
			if scenario == "missing_peer" {
				h.LandingClientRuntime = nil
			}
			requests := 0
			serveRemovalHeadscale(t, s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/node" {
					t.Errorf("approval mutated controller: %s %s", r.Method, r.URL.Path)
				}
				if scenario == "controller_failed" {
					w.WriteHeader(502)
					return
				}
				if scenario == "missing_list" {
					_, _ = w.Write([]byte(`{}`))
					return
				}
				if scenario == "changed_during_inspection" {
					if err := s.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{Version: Version, PublicKey: h.PublicKey, NetworkCandidates: []networking.Candidate{{Address: "10.0.0.99", Kind: "lan", Interface: "eth0"}}}); err != nil {
						t.Error(err)
					}
				}
				item := removalHeadscaleNode{ID: "9", NodeKey: key, IPAddresses: []string{address}, Tags: []string{"tag:vastora-agent"}}
				if scenario == "wrong_owner" {
					item.Tags = nil
				}
				if scenario == "wrong_address" {
					item.NodeKey = "nodekey:someone-else"
				}
				nodes := []removalHeadscaleNode{item}
				if scenario == "ambiguous" {
					item.ID = "10"
					nodes = append(nodes, item)
				}
				if scenario == "old_identity_present" {
					nodes = append(nodes, removalHeadscaleNode{ID: "2", NodeKey: "nodekey:old-key", IPAddresses: []string{"100.64.0.2"}})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"nodes": nodes})
			}))
			var controller string
			if err := s.db.QueryRow(`SELECT endpoint FROM network_integrations WHERE kind='headscale'`).Scan(&controller); err != nil {
				t.Fatal(err)
			}
			if scenario == "controller_changed" {
				controller = "https://previous-controller.example.test"
			}
			old, _ := json.Marshal(reinstallPrivateIdentity{Ownership: "managed", ID: "2", NodeKey: "nodekey:old-key", Address: "100.64.0.2", Endpoint: controller})
			if _, err := s.db.Exec(`UPDATE agent_reinstall_operations SET private_isolation='withdrawn',private_identity_json=? WHERE agent_id=?`, old, node.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordAgentHeartbeat(ctx, node.ID, node.Credential, h); err != nil {
				t.Fatal(err)
			}
			input := networkApprovalInput(t, s, node.ID)
			input.Profile.HeadscaleAddress = address
			input.Profile.EnabledKinds = append(input.Profile.EnabledKinds, "headscale")
			approval, err := s.ApproveAgentReinstallNetwork(ctx, node.ID, "reinstall-review-admin", input)
			if scenario == "valid" || scenario == "same_ip_new_key" {
				if err != nil || approval.PrivatePeer.PublicKey != key || approval.ControllerID != "9" {
					t.Fatalf("verified identity not approved: %+v %v", approval, err)
				}
				if requests != 1 {
					t.Fatalf("unexpected controller calls: %d", requests)
				}
			} else if err == nil {
				t.Fatal("unsafe private identity accepted")
			}
			var live int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM agent_private_peer_capabilities WHERE node_id=?`, node.ID).Scan(&live); err != nil || live != 0 {
				t.Fatalf("unactivated observation became live capability: %d %v", live, err)
			}
		})
	}
}

func TestAgentReinstallAddressApprovalAPIRequiresAdminAndCSRF(t *testing.T) {
	s, node, h := replacementNetworkFixture(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`DELETE FROM admins`); err != nil {
		t.Fatal(err)
	}
	session, csrf, err := s.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	adminID, err := s.SessionAdminID(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE agent_reinstall_operations SET authorized_by=? WHERE agent_id=?`, adminID, node.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordAgentHeartbeat(ctx, node.ID, node.Credential, h); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(networkApprovalInput(t, s, node.ID))
	handler := NewServer(s, "", false).Handler()
	for _, test := range []struct {
		auth, csrf bool
		status     int
	}{{false, false, 401}, {true, false, 401}, {true, true, 200}, {true, true, 200}} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+node.ID+"/reinstall-network/approve", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if test.auth {
			r.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
		}
		if test.csrf {
			r.AddCookie(&http.Cookie{Name: "vastora_csrf", Value: csrf})
			r.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("status %d want %d: %s", w.Code, test.status, w.Body.String())
		}
		if w.Code == 200 && (w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), "10.0.0.8")) {
			t.Fatalf("incorrect approval response: %s", w.Body.String())
		}
	}
}

func TestAgentReinstallAddressApprovalPreservesEntryDependenciesWithoutOldProfile(t *testing.T) {
	for _, kind := range []string{publicationLAN, publicationHeadscale, "public_direct"} {
		t.Run(kind, func(t *testing.T) {
			s, node, h := replacementNetworkFixture(t)
			ctx := context.Background()
			if err := s.RecordAgentHeartbeat(ctx, node.ID, node.Credential, h); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`DELETE FROM agent_network_profile_recovery WHERE agent_id=?`, node.ID); err != nil {
				t.Fatal(err)
			}
			addReinstallApplication(t, s, node, "entry-app", meridianAppKey, "0.1.0-alpha.12", "install", "succeeded")
			stamp := s.now().UTC().Format(time.RFC3339Nano)
			if _, err := s.db.Exec(`INSERT INTO services(id,application_id,site_id,name,protocol,app_protocol,container_port,host_port,endpoint,source,status,created_at,updated_at)
 VALUES('entry-service','entry-app',?,'web','http','http',8080,8080,'http://10.0.0.7:8080','catalog','ready',?,?)`, testSiteID(t, s), stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,dns_provider,status,created_at,updated_at)
 VALUES('entry','entry-service',?,'site_gateway',?,'app.example.test','manual','ready',?,?)`, kind, node.ID, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			input := networkApprovalInput(t, s, node.ID)
			input.Profile = networking.Profile{ServiceAddress: "127.0.0.1", EnabledKinds: []string{}}
			if _, err := s.ApproveAgentReinstallNetwork(ctx, node.ID, "reinstall-review-admin", input); err == nil || !strings.Contains(err.Error(), "saved access entries") {
				t.Fatalf("lost profile erased entry dependency: %v", err)
			}
		})
	}
}
