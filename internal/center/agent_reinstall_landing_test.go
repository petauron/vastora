package center

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
)

func reinstallLandingFixture(t *testing.T, sameAddress bool) (*Store, AgentCredential, AgentCredential, AgentReinstallApplicationInput) {
	t.Helper()
	s, node, input := reinstallRuntimeFixture(t)
	now := s.now().UTC().Format(time.RFC3339Nano)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	old := landing.PeerIdentity{ID: "previous-entry", PublicKey: "nodekey:previous-entry", Address: "100.64.0.7"}
	peer := landing.PeerIdentity{ID: "replacement-entry", PublicKey: "nodekey:replacement-entry", Address: "100.64.0.8"}
	if sameAddress {
		peer.Address = old.Address
	}
	previous, _ := json.Marshal(old)
	current, _ := json.Marshal(peer)
	exec(`UPDATE agents SET tailscale_ownership='managed' WHERE id=?`, node.ID)
	exec(`UPDATE agent_reinstall_operations SET replacement_peer_json=?,replacement_network_observed_at=? WHERE agent_id=?`, current, now, node.ID)
	exec(`INSERT INTO agent_network_candidates(agent_id,address,interface_name,kind,observed_at) VALUES(?,?,'tailscale0','headscale',?)`, node.ID, peer.Address, now)
	// Network approval itself is covered by the controller-backed address tests.
	// Bind the same synthetic approval to the package and operation here.
	var encoded []byte
	if err := s.db.QueryRow(`SELECT approval_json FROM agent_reinstall_app_preparations`).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var approval AgentReinstallNetworkApproval
	if err := json.Unmarshal(encoded, &approval); err != nil {
		t.Fatal(err)
	}
	approval.PrivatePeer = &peer
	approval.Profile.HeadscaleAddress = peer.Address
	approval.Profile.EnabledKinds = append(approval.Profile.EnabledKinds, "headscale")
	approval.ControllerID = "synthetic-controller"
	encoded, _ = json.Marshal(approval)
	exec(`UPDATE agent_reinstall_app_preparations SET approval_json=?`, encoded)
	exec(`UPDATE agent_reinstall_network_approvals SET approval_json=?`, encoded)
	exec(`UPDATE meridian_endpoints SET source_peer_json=? WHERE id='restore-endpoint'`, previous)
	egress := enrollOrchestrationNode(t, s, "reinstall-egress", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "100.64.0.62", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.62", HeadscaleAddress: "100.64.0.62", EnabledKinds: []string{networking.KindHeadscale}})
	exec(`UPDATE agents SET tailscale_ownership='managed',version=? WHERE id=?`, Version, egress.ID)
	server, serverPeer := meridianAppliedLandingFixtureJSON(t, egress.ID, "100.64.0.62", old.Address)
	exec(`INSERT INTO landing_server_states(node_id,desired_revision,applied_revision,desired_json,applied_json,peer_json,status,updated_at) VALUES(?,1,1,?,?,?,'ready',?)`, egress.ID, server, server, serverPeer, now)
	exec(`UPDATE meridian_credentials SET egress_node_id=? WHERE id='route'`, egress.ID)
	exec(`INSERT INTO meridian_route_grants(id,account_id,endpoint_id,egress_node_id,base_credential_id,route_credential_id,enabled,status,created_at,updated_at) VALUES('restore-grant','restore-account','restore-endpoint',?,'native','route',1,'blocked',?,?)`, egress.ID, now, now)
	plan, err := s.AgentReinstallPlan(context.Background(), node.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.PlanRevision = plan.Revision
	return s, node, egress, input
}

func applyReinstallLanding(t *testing.T, s *Store, egress AgentCredential, success bool) {
	t.Helper()
	ctx := context.Background()
	session := "landing-recovery-session"
	if err := s.RegisterExecutionSession(ctx, egress.ID, egress.Credential, session, controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	task, err := s.claimExecutionTask(ctx, egress.ID, egress.Credential, session, 0)
	if err != nil || task == nil || task.Kind != "landing.server.apply" {
		t.Fatalf("claim landing: %+v %v", task, err)
	}
	if err = s.StartExecution(ctx, egress.ID, session, task.Authorization.ID, task.Authorization.Digest); err != nil {
		t.Fatal(err)
	}
	peer := landing.PeerIdentity{ID: "tailnet-" + egress.ID, PublicKey: "nodekey:test-" + egress.ID, Address: "100.64.0.62"}
	body, _ := json.Marshal(map[string]any{"executionId": task.Authorization.ID, "sessionId": session, "attempt": task.Attempt, "succeeded": success, "result": map[string]any{"landingPeer": peer}})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+egress.ID+"/tasks/"+task.ID+"/result", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+egress.Credential)
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	NewServer(s, "", false).Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("result: %d %s", resp.Code, resp.Body.String())
	}
}

func TestAgentReinstallLandingWithdrawalThenAuthorization(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-address", true: "same-address"}[same], func(t *testing.T) {
			s, node, egress, input := reinstallLandingFixture(t, same)
			ctx := context.Background()
			if _, err := s.QueueAgentReinstallRuntime(ctx, node.ID, "reinstall-review-admin", input); err == nil {
				t.Fatal("runtime skipped landing recovery")
			}
			first, err := s.UpdateAgentReinstallLandingSource(ctx, node.ID, "reinstall-review-admin", input, false)
			if err != nil || first.State != "withdrawing" {
				t.Fatalf("withdraw %+v %v", first, err)
			}
			repeat, err := s.UpdateAgentReinstallLandingSource(ctx, node.ID, "reinstall-review-admin", input, false)
			if err != nil || repeat.Landings[0].Revision != first.Landings[0].Revision {
				t.Fatalf("repeat: %+v %v", repeat, err)
			}
			if _, err = s.UpdateAgentReinstallLandingSource(ctx, node.ID, "reinstall-review-admin", input, true); err == nil {
				t.Fatal("accepted desired-only withdrawal")
			}
			applyReinstallLanding(t, s, egress, true)
			plan, err := s.AgentReinstallPlan(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Applications[0].Preparation.Landing.State != "withdrawn" {
				t.Fatalf("not withdrawn: %+v", plan.Applications[0].Preparation.Landing)
			}
			input.PlanRevision = plan.Revision
			second, err := s.UpdateAgentReinstallLandingSource(ctx, node.ID, "reinstall-review-admin", input, true)
			if err != nil || second.State != "authorizing" {
				t.Fatalf("authorize %+v %v", second, err)
			}
			applyReinstallLanding(t, s, egress, true)
			plan, err = s.AgentReinstallPlan(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Applications[0].Preparation.Landing.State != "authorized" {
				t.Fatalf("not authorized: %+v", plan.Applications[0].Preparation.Landing)
			}
			input.PlanRevision = plan.Revision
			receipt, err := s.QueueAgentReinstallRuntime(ctx, node.ID, "reinstall-review-admin", input)
			if err != nil {
				t.Fatal(err)
			}
			task, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
			if err != nil || task == nil || task.ID != receipt.CommandID {
				t.Fatalf("native runtime: %v", err)
			}
			if !strings.Contains(string(task.MeridianRuntime.Desired.Config), restoreRouteID) || task.MeridianRuntime.Source == nil || task.MeridianRuntime.Source.PublicKey != "nodekey:replacement-entry" || len(task.MeridianRuntime.Peers) != 1 || task.MeridianRuntime.Peers[0].EgressID != egress.ID {
				t.Fatal("reviewed fixed route or exact replacement identity missing from restored runtime")
			}
			if resp := submitRestoredRuntime(t, s, node, task, true, true); resp.Code != http.StatusOK {
				t.Fatal(resp.Body.String())
			}
			var healthy int
			if err = s.db.QueryRow(`SELECT runtime_healthy FROM meridian_route_grants WHERE id='restore-grant'`).Scan(&healthy); err != nil || healthy != 0 {
				t.Fatalf("invented route health %d %v", healthy, err)
			}
			if blocked, err := agentReinstallBlocked(ctx, s.db, node.ID); err != nil || !blocked {
				t.Fatal("released recovery fence")
			}
			directory := s.dataDir
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			repeat, err = s.UpdateAgentReinstallLandingSource(ctx, node.ID, "reinstall-review-admin", input, true)
			if err != nil || repeat.State != "authorized" {
				t.Fatalf("restart repeat %+v %v", repeat, err)
			}
			var count int
			if err = s.db.QueryRow(`SELECT COUNT(*) FROM task_events WHERE kind='meridian.source.recover'`).Scan(&count); err != nil || count != 1 {
				t.Fatal("handoff replayed")
			}
		})
	}
}

func TestAgentReinstallLandingStopsOnChangedOrFailedEvidence(t *testing.T) {
	for _, mode := range []string{"admin", "plan", "private-key", "stale", "future", "grant", "landing-peer", "failed", "shared-source"} {
		t.Run(mode, func(t *testing.T) {
			s, node, egress, input := reinstallLandingFixture(t, false)
			ctx := context.Background()
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := s.db.Exec(q, args...); err != nil {
					t.Fatal(err)
				}
			}
			admin := "reinstall-review-admin"
			if mode == "admin" {
				admin = "missing-admin"
			}
			if mode == "plan" {
				input.PlanRevision = strings.Repeat("0", 64)
			}
			if mode == "shared-source" {
				// Another independently managed proxy still needs the old address. Recovery
				// must not delete its permission or falsely claim withdrawal.
				exec(`INSERT INTO landing_proxy_states(node_id,application_id,landing_node_id,server_revision,source_address,desired_revision,desired_json,status,updated_at) VALUES(?, 'retained-meridian',?,1,'100.64.0.7',1,'{"proxy":{}}','ready',?)`, node.ID, egress.ID, s.now().UTC().Format(time.RFC3339Nano))
				plan, e := s.AgentReinstallPlan(ctx, node.ID)
				if e != nil {
					t.Fatal(e)
				}
				input.PlanRevision = plan.Revision
			}
			first, err := s.UpdateAgentReinstallLandingSource(ctx, node.ID, admin, input, false)
			if mode == "admin" || mode == "plan" || mode == "shared-source" {
				if err == nil {
					t.Fatal("unreviewed withdrawal accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "failed" {
				applyReinstallLanding(t, s, egress, false)
			} else {
				applyReinstallLanding(t, s, egress, true)
			}
			switch mode {
			case "private-key":
				exec(`UPDATE agent_reinstall_operations SET replacement_peer_json=json_set(replacement_peer_json,'$.publicKey','nodekey:unapproved')`)
			case "stale":
				exec(`UPDATE agent_reinstall_operations SET replacement_network_observed_at=?`, s.now().Add(-time.Hour).UTC().Format(time.RFC3339Nano))
			case "future":
				exec(`UPDATE agent_reinstall_operations SET replacement_network_observed_at=?`, s.now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
			case "grant":
				exec(`UPDATE meridian_route_grants SET enabled=0,status='revoked'`)
			case "landing-peer":
				exec(`UPDATE landing_server_states SET peer_json=json_set(peer_json,'$.publicKey','nodekey:changed')`)
			}
			plan, err := s.AgentReinstallPlan(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			input.PlanRevision = plan.Revision
			if _, err = s.UpdateAgentReinstallLandingSource(ctx, node.ID, "reinstall-review-admin", input, true); err == nil {
				t.Fatal("unsafe handoff accepted")
			}
			repeat, err := s.UpdateAgentReinstallLandingSource(ctx, node.ID, "reinstall-review-admin", input, false)
			if err == nil && repeat.Landings[0].Revision != first.Landings[0].Revision {
				t.Fatal("failed withdrawal retried")
			}
			if _, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0); !errors.Is(err, errExecutionBlocked) {
				t.Fatalf("entry unfenced: %v", err)
			}
		})
	}
}

func TestAgentReinstallLandingPreservesIndependentSources(t *testing.T) {
	s, node, egress, input := reinstallLandingFixture(t, false)
	ctx := context.Background()
	if _, err := s.db.Exec(`INSERT INTO landing_proxy_states(node_id,application_id,landing_node_id,server_revision,source_address,desired_revision,desired_json,status,updated_at) VALUES(?, 'retained-meridian',?,1,'100.64.0.44',1,'{"proxy":{}}','ready',?)`, egress.ID, egress.ID, s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	desired, applied, _ := readMeridianAuthorizationServer(t, s, egress.ID)
	desired.Plan.Sources = append(desired.Plan.Sources, landing.AuthorizedNode{Address: "100.64.0.44"})
	applied.Plan.Sources = desired.Plan.Sources
	writeMeridianAuthorizationServer(t, s, desired, applied, "ready")
	for _, authorize := range []bool{false, true} {
		plan, err := s.AgentReinstallPlan(ctx, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		input.PlanRevision = plan.Revision
		if _, err = s.UpdateAgentReinstallLandingSource(ctx, node.ID, "reinstall-review-admin", input, authorize); err != nil {
			t.Fatal(err)
		}
		applyReinstallLanding(t, s, egress, true)
		_, applied, _ = readMeridianAuthorizationServer(t, s, egress.ID)
		preserved := false
		for _, source := range applied.Plan.Sources {
			if source.Address == "100.64.0.44" && !source.TCPOnly {
				preserved = true
			}
		}
		if !preserved {
			t.Fatal("recovery removed or narrowed an independent proxy source")
		}
	}
}

func TestAgentReinstallLandingClaimDoesNotGrantChangedAuthority(t *testing.T) {
	for _, mode := range []string{"replacement-key", "receipt", "landing-peer"} {
		t.Run(mode, func(t *testing.T) {
			s, node, egress, input := reinstallLandingFixture(t, false)
			ctx := context.Background()
			if _, err := s.UpdateAgentReinstallLandingSource(ctx, node.ID, "reinstall-review-admin", input, false); err != nil {
				t.Fatal(err)
			}
			applyReinstallLanding(t, s, egress, true)
			plan, err := s.AgentReinstallPlan(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			input.PlanRevision = plan.Revision
			if _, err = s.UpdateAgentReinstallLandingSource(ctx, node.ID, "reinstall-review-admin", input, true); err != nil {
				t.Fatal(err)
			}
			query := `UPDATE agent_reinstall_operations SET replacement_peer_json=json_set(replacement_peer_json,'$.publicKey','nodekey:unreviewed')`
			if mode == "receipt" {
				query = `DELETE FROM agent_reinstall_landing_sources`
			}
			if mode == "landing-peer" {
				query = `UPDATE landing_server_states SET peer_json=json_set(peer_json,'$.publicKey','nodekey:unreviewed')`
			}
			if _, err = s.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			// The normal landing claim rebuilds the shared authorization union and
			// removes an approval that ceased to match, even after it was queued.
			applyReinstallLanding(t, s, egress, true)
			_, applied, _ := readMeridianAuthorizationServer(t, s, egress.ID)
			for _, source := range applied.Plan.Sources {
				if source.Address == "100.64.0.8" || source.Address == "100.64.0.7" {
					t.Fatal("claim retained an unreviewed source")
				}
			}
			plan, err = s.AgentReinstallPlan(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Applications[0].Preparation.Landing.State == "authorized" {
				t.Fatal("changed authority reported applied")
			}
		})
	}
}

func queueReinstallLandingRuntime(t *testing.T) (*Store, AgentCredential, AgentReinstallApplicationInput) {
	t.Helper()
	s, node, egress, input := reinstallLandingFixture(t, false)
	ctx := context.Background()
	for _, authorize := range []bool{false, true} {
		plan, err := s.AgentReinstallPlan(ctx, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		input.PlanRevision = plan.Revision
		if _, err = s.UpdateAgentReinstallLandingSource(ctx, node.ID, "reinstall-review-admin", input, authorize); err != nil {
			t.Fatal(err)
		}
		applyReinstallLanding(t, s, egress, true)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.PlanRevision = plan.Revision
	if _, err = s.QueueAgentReinstallRuntime(ctx, node.ID, "reinstall-review-admin", input); err != nil {
		t.Fatal(err)
	}
	return s, node, input
}

func TestAgentReinstallLandingRuntimeRejectsChangedAuthorization(t *testing.T) {
	for _, stage := range []string{"claim", "result"} {
		t.Run(stage, func(t *testing.T) {
			s, node, _ := queueReinstallLandingRuntime(t)
			ctx := context.Background()
			var task *AgentTask
			var err error
			if stage == "result" {
				task, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
				if err != nil || task == nil {
					t.Fatal(err)
				}
			}
			if _, err = s.db.Exec(`DELETE FROM agent_reinstall_landing_sources`); err != nil {
				t.Fatal(err)
			}
			if stage == "claim" {
				task, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
				if err == nil && task != nil {
					t.Fatal("runtime claimed missing landing approval")
				}
			} else {
				response := submitRestoredRuntime(t, s, node, task, true, true)
				if response.Code == http.StatusOK {
					t.Fatal("runtime projected with missing landing approval")
				}
			}
			var healthy int
			if err = s.db.QueryRow(`SELECT runtime_healthy FROM meridian_endpoints WHERE id='restore-endpoint'`).Scan(&healthy); err != nil || healthy != 0 {
				t.Fatal("invalid authority marked runtime healthy")
			}
		})
	}
}

func TestAgentReinstallLandingRuntimeRequiresExactHealthyTransport(t *testing.T) {
	for _, mode := range []string{"healthy", "blocked", "stale", "wrong-source", "missing-peer"} {
		t.Run(mode, func(t *testing.T) {
			s, node, _ := queueReinstallLandingRuntime(t)
			ctx := context.Background()
			task, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
			if err != nil || task == nil {
				t.Fatal(err)
			}
			result := meridianHealthResult(meridianRuntimeProjection{task: *task.MeridianRuntime}, s.now().UTC(), true)
			switch mode {
			case "blocked":
				result.Peers[0].Status.State = "blocked"
			case "stale":
				result.Peers[0].Status.CheckedAt = s.now().Add(-time.Hour)
			case "wrong-source":
				result.Source = &landing.PeerIdentity{ID: "other", PublicKey: "nodekey:other", Address: "100.64.0.8"}
			case "missing-peer":
				result.Peers = nil
			}
			response := submitReinstallRuntimeResult(t, s, node, task, result, true)
			invalid := mode == "wrong-source" || mode == "missing-peer"
			if (response.Code != http.StatusOK) != invalid {
				t.Fatalf("result %s: %d %s", mode, response.Code, response.Body.String())
			}
			plan, err := s.AgentReinstallPlan(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			state := plan.Applications[0].Preparation.Runtime.State
			if mode == "healthy" && state != "succeeded" || mode != "healthy" && state == "succeeded" {
				t.Fatalf("runtime state %s: %s", mode, state)
			}
			if !invalid {
				var body string
				if err = s.db.QueryRow(`SELECT result_json FROM application_commands WHERE id=?`, task.ID).Scan(&body); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(body, "replacement-entry") || !strings.Contains(body, "receipt") {
					t.Fatal("discarded applied runtime or transport evidence")
				}
			}
			if plan.Recovery.State != "review_required" {
				t.Fatal("transport receipt finished client acceptance")
			}
			if mode == "blocked" || mode == "stale" {
				input := AgentReinstallApplicationInput{OperationID: plan.Recovery.ID, ApplicationID: "retained-meridian", PlanRevision: plan.Revision}
				if _, err = s.ActivateAgentReinstallAccess(ctx, node.ID, "reinstall-review-admin", input); err == nil {
					t.Fatal("activated address with unverified transport")
				}
				var commandState string
				if err = s.db.QueryRow(`SELECT state FROM application_commands WHERE id=?`, task.ID).Scan(&commandState); err != nil || commandState != "succeeded" {
					t.Fatal("rewrote applied execution as failure")
				}
			}
		})
	}
}
