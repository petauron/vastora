package center

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
)

func TestExecutionUpdateHandoffIsAtomicAndSingleUse(t *testing.T) {
	for _, mode := range []string{"handoff", "lease-conflict", "expired", "abandon", "confirm-completed"} {
		t.Run(mode, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "helper-test", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.19", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.19", LANAddress: "10.0.0.19", EnabledKinds: []string{networking.KindLAN}})
			heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.123", true)
			if _, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.124"); err != nil {
				t.Fatal(err)
			}
			task, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
			if err != nil || task == nil {
				t.Fatalf("claim: %v %v", task, err)
			}
			session := "helper-original-process-session"
			if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
				t.Fatal(err)
			}
			auth, err := store.PersistExecutionAuthorization(ctx, node.ID, session, *task)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.StartExecution(ctx, node.ID, session, auth.ID, auth.Digest); err != nil {
				t.Fatal(err)
			}
			begin := func(id, sid string) error {
				return store.beginAgentUpdateExecution(ctx, node.ID, node.Credential, task.ID, task.Attempt, id, sid)
			}
			if err := begin(auth.ID, session); !errors.Is(err, errExecutionAuthorization) {
				t.Fatalf("handoff without phase: %v", err)
			}
			if err := store.CheckExecutionStep(ctx, node.ID, session, auth.ID, "handoff"); err != nil {
				t.Fatal(err)
			}
			if err := begin(auth.ID, "obsolete-process-session"); !errors.Is(err, errExecutionAuthorization) {
				t.Fatalf("obsolete session: %v", err)
			}
			if mode == "lease-conflict" {
				if _, err := store.db.Exec(`UPDATE agent_updates SET lease_expires_at='' WHERE id=?`, task.ID); err != nil {
					t.Fatal(err)
				}
				if err := begin(auth.ID, session); !errors.Is(err, errStaleTaskLease) {
					t.Fatalf("lease conflict: %v", err)
				}
				var state, phase string
				if err := store.db.QueryRow(`SELECT state,phase FROM task_executions WHERE id=?`, auth.ID).Scan(&state, &phase); err != nil || state != "running" || phase != "handoff" {
					t.Fatalf("failed transaction consumed grant: %s/%s %v", state, phase, err)
				}
				return
			}
			handler := NewServer(store, "", false).Handler()
			post := func(path string, input any, want int) {
				t.Helper()
				body, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+node.Credential)
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != want {
					t.Fatalf("helper request %s: %d want %d: %s", path, w.Code, want, w.Body.String())
				}
			}
			post("/api/v1/agents/"+node.ID+"/updates/"+task.ID+"/start", map[string]any{"attempt": task.Attempt, "executionId": auth.ID, "sessionId": session}, http.StatusOK)
			if err := store.StoreExecutionResult(ctx, node.ID, session, auth.ID, json.RawMessage(`{}`), true, false, "", nil); !errors.Is(err, errExecutionAuthorization) {
				t.Fatalf("helper succeeded without authorized start: %v", err)
			}
			stepPath := "/api/v1/agents/" + node.ID + "/executions/" + auth.ID
			post(stepPath, controlplane.ExecutionTransitionRequest{SessionID: session, Action: "helper-step", Phase: "install"}, http.StatusConflict)
			for _, phase := range []string{"stop", "backup", "preserve", "install", "start"} {
				step := controlplane.ExecutionTransitionRequest{SessionID: session, Action: "helper-step", Phase: phase}
				post(stepPath, step, http.StatusOK)
				post(stepPath, step, http.StatusConflict)
			}
			if err := begin(auth.ID, session); !errors.Is(err, errExecutionAuthorization) {
				t.Fatalf("double consumption: %v", err)
			}
			if err := store.CheckExecutionStep(ctx, node.ID, session, auth.ID, "apply"); !errors.Is(err, errExecutionAuthorization) {
				t.Fatalf("Agent kept mutation authority after handoff: %v", err)
			}
			if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, "helper-replacement-agent-session", controlplane.ExecutionProtocol); err != nil {
				t.Fatal(err)
			}
			views, err := executionViewsForTest(ctx, store)
			if err != nil || len(views) != 1 || views[0].State != "helper_running" {
				t.Fatalf("helper ownership lost on Agent restart: %v %v", views, err)
			}
			if _, err := store.ClaimNextTask(ctx, node.ID, node.Credential); !errors.Is(err, errExecutionBlocked) {
				t.Fatalf("new work bypassed helper: %v", err)
			}
			if ready, err := store.UpdateHelperObserved(ctx, node.ID, session, auth.ID); err != nil || ready {
				t.Fatalf("source version treated as updated: ready=%v err=%v", ready, err)
			}
			if _, err := store.UpdateHelperObserved(ctx, node.ID, "wrong-session", auth.ID); !errors.Is(err, errExecutionAuthorization) {
				t.Fatalf("observation accepted wrong authorization: %v", err)
			}
			// Only the successful handoff has installed-version evidence. The
			// expiry/disposition cases must keep the old version until recovery.
			if mode == "handoff" {
				heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.124", true)
				if ready, err := store.UpdateHelperObserved(ctx, node.ID, session, auth.ID); err != nil || !ready {
					t.Fatalf("target heartbeat not observed: ready=%v err=%v", ready, err)
				}
			}
			result := map[string]any{"attempt": task.Attempt, "executionId": auth.ID, "sessionId": session, "succeeded": true, "result": map[string]any{}}
			if mode == "abandon" || mode == "confirm-completed" {
				expired := store.now().Add(31 * time.Minute)
				store.now = func() time.Time { return expired }
				if err := store.executionClaimAllowed(ctx, node.ID, "helper-replacement-agent-session"); !errors.Is(err, errExecutionBlocked) {
					t.Fatalf("expiry: %v", err)
				}
				sessionCookie, csrf, err := store.CreateFirstAdmin(ctx, "helper-admin", "test-only-long-password")
				if err != nil {
					t.Fatal(err)
				}
				adminID, err := store.SessionAdminID(ctx, sessionCookie)
				if err != nil {
					t.Fatal(err)
				}
				decision := controlplane.ExecutionDisposition{Action: mode, ExecutionStopped: true, Note: "Verified helper stopped and checked installed state"}
				if err := store.DisposeHelperExecution(ctx, auth.ID, "not-admin", decision); err == nil {
					t.Fatal("non-admin disposed execution")
				}
				unconfirmed := decision
				unconfirmed.ExecutionStopped = false
				if err := store.DisposeHelperExecution(ctx, auth.ID, adminID, unconfirmed); err == nil {
					t.Fatal("disposition without stopped confirmation")
				}
				if mode == "confirm-completed" {
					if err := store.DisposeHelperExecution(ctx, auth.ID, adminID, decision); err == nil {
						t.Fatal("stale observation accepted")
					}
				}
				observedVersion := "0.1.0-alpha.123"
				if mode == "confirm-completed" {
					observedVersion = "0.1.0-alpha.124"
				}
				heartbeatAgentUpdateVersion(t, store, node, observedVersion, true)
				body, _ := json.Marshal(decision)
				r := httptest.NewRequest(http.MethodPost, "/api/v1/executions/"+auth.ID+"/resolve-helper", bytes.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-CSRF-Token", csrf)
				r.AddCookie(&http.Cookie{Name: "vastora_session", Value: sessionCookie})
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != http.StatusOK {
					t.Fatalf("resolve: %d %s", w.Code, w.Body.String())
				}
				if err := store.DisposeHelperExecution(ctx, auth.ID, adminID, decision); err == nil {
					t.Fatal("duplicate disposition")
				}
				views, err = executionViewsForTest(ctx, store)
				if err != nil || views[0].State != "unknown" || views[0].Disposition != mode {
					t.Fatalf("original evidence overwritten: %v %v", views, err)
				}
				post("/api/v1/agents/"+node.ID+"/tasks/"+task.ID+"/result", result, http.StatusConflict)
				if mode == "abandon" {
					if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.125"); err != nil || len(queued) != 1 {
						t.Fatalf("verified abandoned intent was not replaced by a fresh rollout: %v %v", queued, err)
					}
				}
				return
			}
			if mode == "expired" {
				expired := store.now().Add(31 * time.Minute)
				store.now = func() time.Time { return expired }
				post("/api/v1/agents/"+node.ID+"/tasks/"+task.ID+"/result", result, http.StatusConflict)
				if err := store.executionClaimAllowed(ctx, node.ID, "helper-replacement-agent-session"); !errors.Is(err, errExecutionBlocked) {
					t.Fatalf("expired helper released fence: %v", err)
				}
				views, err = executionViewsForTest(ctx, store)
				if err != nil || len(views) != 1 || views[0].State != "unknown" {
					t.Fatalf("expired helper not unknown: %v %v", views, err)
				}
				post(stepPath, controlplane.ExecutionTransitionRequest{SessionID: session, Action: "helper-step", Phase: "start"}, http.StatusConflict)
				post("/api/v1/agents/"+node.ID+"/tasks/"+task.ID+"/result", result, http.StatusConflict)
				return
			}
			post("/api/v1/agents/"+node.ID+"/tasks/"+task.ID+"/result", result, http.StatusOK)
			views, err = executionViewsForTest(ctx, store)
			if err != nil || len(views) != 1 || views[0].State != "succeeded" {
				t.Fatalf("helper result after Agent restart: %v %v", views, err)
			}
			post("/api/v1/agents/"+node.ID+"/tasks/"+task.ID+"/result", result, http.StatusConflict)
		})
	}
}
