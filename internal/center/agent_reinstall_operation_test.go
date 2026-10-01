package center

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
)

func prepareReinstallNode(t *testing.T) (*Store, AgentCredential) {
	t.Helper()
	store, node := reinstallPlanFixture(t)
	if _, err := store.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?),(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, agentConnectionModeSetting, "lan", agentConnectURLSetting, "https://center.example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE agents SET last_seen_at='2020-01-01T00:00:00Z' WHERE id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	return store, node
}

func TestAgentReinstallRejectsUnreviewedOrChangedPlan(t *testing.T) {
	store, node := prepareReinstallNode(t)
	ctx := context.Background()
	input := reviewedReconnectInput(t, store, node.ID)
	for _, bad := range []AgentReinstallInput{{}, {OperationID: input.OperationID, PlanRevision: input.PlanRevision}} {
		if _, err := store.CreateAgentReconnectEnrollment(ctx, node.ID, "reinstall-review-admin", bad); err == nil {
			t.Fatal("unconfirmed replacement accepted")
		}
	}
	if _, err := store.CreateAgentReconnectEnrollment(ctx, node.ID, "unknown-admin", input); err == nil {
		t.Fatal("unknown administrator accepted")
	}
	addReinstallApplication(t, store, node, "changed-app", meridianAppKey, "0.1.0-alpha.12", "install", "succeeded")
	if _, err := store.CreateAgentReconnectEnrollment(ctx, node.ID, "reinstall-review-admin", input); err == nil {
		t.Fatal("stale review accepted")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_reinstall_operations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected review mutated recovery: %d %v", count, err)
	}
	if err := store.authenticateAgent(ctx, node.ID, node.Credential); err != nil {
		t.Fatalf("rejected review revoked credential: %v", err)
	}
}

func TestAgentReinstallReplaysGrantAfterCenterRestart(t *testing.T) {
	store, node := prepareReinstallNode(t)
	ctx := context.Background()
	input := reviewedReconnectInput(t, store, node.ID)
	enrollment, err := store.CreateAgentReconnectEnrollment(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil {
		t.Fatal(err)
	}
	var databasePath string
	var sequence int
	var name string
	if err = store.db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &databasePath); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(filepath.Dir(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replay, err := reopened.CreateAgentReconnectEnrollment(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || replay != enrollment {
		t.Fatalf("lost response generated another command: %#v %v", replay, err)
	}
	var count int
	if err = reopened.db.QueryRow(`SELECT COUNT(*) FROM agent_reinstall_operations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate recovery: %d %v", count, err)
	}
	plan, err := reopened.AgentReinstallPlan(ctx, node.ID)
	if err != nil || plan.Recovery == nil || plan.Recovery.AuthorizedBy != "reinstall-review-admin" || plan.Recovery.State != "awaiting_enrollment" {
		t.Fatalf("authorization not durable: %+v %v", plan.Recovery, err)
	}
}

func TestAgentReinstallFencesPendingWorkAndRuntimeRebuild(t *testing.T) {
	store, node := prepareReinstallNode(t)
	ctx := context.Background()
	addReinstallApplication(t, store, node, "saved-app", meridianAppKey, "0.1.0-alpha.12", "install", "succeeded")
	addReinstallApplication(t, store, node, "pending-app", pulseAgentAppKey, "0.1.0-alpha.5", "install", "pending")
	if _, err := store.db.Exec(`INSERT INTO agent_network_profiles(agent_id,service_address,lan_address,enabled_kinds_json,direct_public,confirmed_at,candidate_observed_at) VALUES(?,'10.0.0.7','10.0.0.7','["lan"]',0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, node.ID); err != nil {
		t.Fatal(err)
	}
	setReinstallPrivateObservation(t, store, node.ID)
	serveRemovalHeadscale(t, store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/node" {
			t.Errorf("unexpected private mutation: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"nodes":[]}`))
	}))
	if _, err := store.db.Exec(`UPDATE agent_network_profiles SET headscale_address='100.64.0.2' WHERE agent_id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	enrollment, err := createReviewedReconnect(t, store, ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	key := testAgentPublicKey(t)
	replacement, err := store.EnrollAgent(ctx, enrollment.Token, Version, "linux", "amd64", key)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RegisterExecutionSession(ctx, node.ID, replacement.Credential, "replacement-machine-execution-session", controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	if err = store.executionClaimAllowed(ctx, node.ID, "replacement-machine-execution-session"); !errors.Is(err, errExecutionBlocked) {
		t.Fatalf("execution claim not fenced: %v", err)
	}
	if _, err = store.ClaimNextTask(ctx, node.ID, replacement.Credential); !errors.Is(err, errExecutionBlocked) {
		t.Fatalf("unclaimed old task not fenced: %v", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"application.apply", "agent.update", "xray.configuration.inspect", "xray.configuration.apply"} {
		_, err := store.persistExecutionAuthorization(ctx, tx, node.ID, "replacement-machine-execution-session", AgentTask{ID: "previous-intent", Kind: kind, Attempt: 1})
		if !errors.Is(err, errExecutionBlocked) {
			t.Fatalf("%s bypassed recovery: %v", kind, err)
		}
	}
	tx.Rollback()
	err = store.RecordAgentHeartbeat(ctx, node.ID, replacement.Credential, NodeHeartbeat{Version: Version, PublicKey: key, Startup: true, ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration,
		Capabilities: NodeCapabilities{Docker: true}, Roles: []string{"worker"}, NetworkCandidates: []networking.Candidate{{Address: "10.0.0.7", Interface: "eth0", Kind: "lan"}}})
	if err != nil {
		t.Fatal(err)
	}
	var deployments, profiles, retained int
	if err = store.db.QueryRow(`SELECT COUNT(*) FROM deployments`).Scan(&deployments); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT COUNT(*) FROM agent_network_profiles WHERE agent_id=?`, node.ID).Scan(&profiles); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT COUNT(*) FROM agent_network_profile_recovery r JOIN agents a ON a.id=r.agent_id WHERE r.agent_id=? AND r.public_key<>a.x25519_public_key`, node.ID).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if deployments != 2 || profiles != 0 || retained != 1 {
		t.Fatalf("heartbeat rebuilt old intent or accepted old network: deployments=%d profiles=%d retained=%d", deployments, profiles, retained)
	}
	plan, err := store.AgentReinstallPlan(ctx, node.ID)
	if err != nil || plan.Recovery == nil || plan.Recovery.State != "review_required" || plan.Recovery.ReplacementFingerprint == "" {
		t.Fatalf("online was mistaken for complete: %+v %v", plan.Recovery, err)
	}
	if plan.PrivateNetwork.Ownership != "managed" || plan.PrivateNetwork.AddressRecovery != "explicit_migration_required" {
		t.Fatalf("replacement erased old network isolation requirements: %+v", plan.PrivateNetwork)
	}
	agents, err := store.ListAgents(ctx)
	if err != nil || len(agents) != 1 || agents[0].Reinstall == nil || !agents[0].Connected {
		t.Fatalf("recovery not visible on online node: %+v %v", agents, err)
	}
}

func TestAgentReinstallRejectsOldKeyAndStopsInterruptedPreparation(t *testing.T) {
	for _, scenario := range []string{"old_key", "interrupted", "access_stopped", "bootstrap_failed"} {
		t.Run(scenario, func(t *testing.T) {
			store, node := prepareReinstallNode(t)
			ctx := context.Background()
			input := reviewedReconnectInput(t, store, node.ID)
			if scenario == "old_key" {
				var oldKey []byte
				if err := store.db.QueryRow(`SELECT x25519_public_key FROM agents WHERE id=?`, node.ID).Scan(&oldKey); err != nil {
					t.Fatal(err)
				}
				enrollment, err := store.CreateAgentReconnectEnrollment(ctx, node.ID, "reinstall-review-admin", input)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = store.EnrollAgent(ctx, enrollment.Token, Version, "linux", "amd64", oldKey); err == nil {
					t.Fatal("old machine key accepted as replacement")
				}
				if _, err = store.EnrollAgent(ctx, enrollment.Token, Version, "linux", "amd64", testAgentPublicKey(t)); err != nil {
					t.Fatal(err)
				}
				return
			}
			if scenario == "bootstrap_failed" {
				if _, err := store.db.Exec(`UPDATE settings SET value='headscale' WHERE key=?`, agentConnectionModeSetting); err != nil {
					t.Fatal(err)
				}
				if _, err := store.CreateAgentReconnectEnrollment(ctx, node.ID, "reinstall-review-admin", input); err == nil {
					t.Fatal("unconfigured private controller accepted")
				}
			} else {
				if _, err := store.beginAgentReinstall(ctx, node.ID, "reinstall-review-admin", input); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "access_stopped" {
				if err := store.RevokeAgentCredential(ctx, node.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := store.createAgentReconnectEnrollment(ctx, node.ID, input.OperationID); err == nil {
					t.Fatal("revoked in-flight recovery created a grant")
				}
			}
			if _, err := store.CreateAgentReconnectEnrollment(ctx, node.ID, "reinstall-review-admin", input); err == nil {
				t.Fatal("interrupted recovery silently replayed")
			}
			var grants int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM agent_enrollment_tokens WHERE target_agent_id=?`, node.ID).Scan(&grants); err != nil || grants != 0 {
				t.Fatalf("unexpected recovery grant: %d %v", grants, err)
			}
			if scenario != "access_stopped" {
				blocked, err := agentReinstallBlocked(ctx, store.db, node.ID)
				if err != nil || !blocked {
					t.Fatalf("failed recovery released fence: %v", err)
				}
			}
		})
	}
}

func TestAgentReinstallUnaffectedNodeMayStillClaim(t *testing.T) {
	store, node := prepareReinstallNode(t)
	otherEnrollment, err := store.CreateAgentEnrollment(context.Background(), AgentEnrollmentSpec{SiteID: testSiteID(t, store), Name: "Other", CenterURL: "https://center.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.EnrollAgent(context.Background(), otherEnrollment.Token, Version, "linux", "amd64", testAgentPublicKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = createReviewedReconnect(t, store, context.Background(), node.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.RegisterExecutionSession(context.Background(), other.ID, other.Credential, "unchanged-machine-execution-session", controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	if err = store.executionClaimAllowed(context.Background(), other.ID, "unchanged-machine-execution-session"); err != nil {
		t.Fatal(err)
	}
}

func TestAgentReinstallAPIRequiresReviewedAdminConfirmation(t *testing.T) {
	store, node := prepareReinstallNode(t)
	ctx := context.Background()
	session, csrf, err := store.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	adminID, err := store.SessionAdminID(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := store.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := AgentReinstallInput{OperationID: "reviewed-operation-example", PlanRevision: plan.Revision, ConfirmReplacement: true}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(store, "", false).Handler()
	for _, scenario := range []struct {
		auth, csrf bool
		body       []byte
		status     int
	}{
		{false, false, encoded, 401}, {true, false, encoded, 401}, {true, true, []byte(`{}`), 400}, {true, true, encoded, 201},
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+node.ID+"/reconnect", bytes.NewReader(scenario.body))
		r.Header.Set("Content-Type", "application/json")
		if scenario.auth {
			r.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
		}
		if scenario.csrf {
			r.AddCookie(&http.Cookie{Name: "vastora_csrf", Value: csrf})
			r.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != scenario.status {
			t.Fatalf("status %d want %d: %s", w.Code, scenario.status, w.Body.String())
		}
		if w.Code == 201 && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("command response can be cached")
		}
	}
	plan, err = store.AgentReinstallPlan(ctx, node.ID)
	if err != nil || plan.Recovery == nil || plan.Recovery.AuthorizedBy != adminID {
		t.Fatalf("request administrator not recorded: %+v %v", plan.Recovery, err)
	}
}

func TestAgentReinstallIsolationContinuationAPIRequiresCurrentAuthorization(t *testing.T) {
	store, node := prepareReinstallNode(t)
	ctx := context.Background()
	session, csrf, err := store.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	adminID, err := store.SessionAdminID(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	input := reviewedReconnectInput(t, store, node.ID)
	if _, err = store.beginAgentReinstall(ctx, node.ID, adminID, input); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(AgentReinstallContinueInput{OperationID: input.OperationID, ExpectedAttempt: 1, ConfirmIsolation: true})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(store, "", false).Handler()
	for _, scenario := range []struct {
		auth, csrf bool
		body       []byte
		status     int
	}{
		{false, false, encoded, 401}, {true, false, encoded, 401}, {true, true, []byte(`{}`), 400}, {true, true, encoded, 201}, {true, true, encoded, 400},
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+node.ID+"/reinstall-isolation/continue", bytes.NewReader(scenario.body))
		r.Header.Set("Content-Type", "application/json")
		if scenario.auth {
			r.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
		}
		if scenario.csrf {
			r.AddCookie(&http.Cookie{Name: "vastora_csrf", Value: csrf})
			r.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != scenario.status {
			t.Fatalf("status %d want %d: %s", w.Code, scenario.status, w.Body.String())
		}
		if w.Code == 201 && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("continued command can be cached")
		}
	}
}
