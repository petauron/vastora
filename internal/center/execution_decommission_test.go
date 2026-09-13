package center

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"encoding/json"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/secret"
)

func TestExecutionDecommissionPublicStepAndResult(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	configureDecommissionCallbackForTest(t, store)
	node := enrollOrchestrationNode(t, store, "public-cleanup", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.20", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.20", LANAddress: "10.0.0.20", EnabledKinds: []string{networking.KindLAN}})
	if err := store.queueAgentDecommission(ctx, node.ID, true); err != nil {
		t.Fatal(err)
	}
	task := waitForDecommissionTask(t, store, node)
	id, session := authorizeDecommissionForTest(t, store, node, *task)
	if err := store.beginAgentDecommission(ctx, node.ID, node.Credential, task.ID, task.Attempt, id, session); err != nil {
		t.Fatal(err)
	}
	handler := NewServer(store, "", false).Handler()
	post := func(token string, body map[string]any, status int) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, task.DecommissionCallbackURL, bytes.NewReader(raw))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != status {
			t.Fatalf("callback status=%d want=%d body=%s", response.Code, status, response.Body.String())
		}
	}
	step := map[string]any{"action": "step", "attempt": task.Attempt, "sequence": 1, "phase": "command"}
	post(node.Credential, step, http.StatusConflict)
	post(task.DecommissionCallbackToken, step, http.StatusOK)
	post(task.DecommissionCallbackToken, step, http.StatusConflict)
	post(task.DecommissionCallbackToken, map[string]any{"action": "step", "attempt": task.Attempt, "sequence": 2, "phase": "done"}, http.StatusOK)
	post(task.DecommissionCallbackToken, map[string]any{"attempt": task.Attempt}, http.StatusOK)
	post(task.DecommissionCallbackToken, step, http.StatusConflict)
}

func TestExecutionDecommissionOperatorDisposition(t *testing.T) {
	for _, action := range []string{"abandon", "confirm-completed"} {
		t.Run(action, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			configureDecommissionCallbackForTest(t, store)
			node := enrollOrchestrationNode(t, store, "disposition-test", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.21", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.21", LANAddress: "10.0.0.21", EnabledKinds: []string{networking.KindLAN}})
			if err := store.queueAgentDecommission(ctx, node.ID, true); err != nil {
				t.Fatal(err)
			}
			task := waitForDecommissionTask(t, store, node)
			id, session := authorizeDecommissionForTest(t, store, node, *task)
			if err := store.beginAgentDecommission(ctx, node.ID, node.Credential, task.ID, task.Attempt, id, session); err != nil {
				t.Fatal(err)
			}
			if err := store.completeAgentDecommissionCallback(ctx, task.ID, task.DecommissionCallbackToken, task.Attempt, "cleanup response lost"); err != nil {
				t.Fatal(err)
			}
			cookie, csrf, err := store.CreateFirstAdmin(ctx, "operator", "test-only-long-password")
			if err != nil {
				t.Fatal(err)
			}
			adminID, err := store.SessionAdminID(ctx, cookie)
			if err != nil {
				t.Fatal(err)
			}
			decision := controlplane.ExecutionDisposition{Action: action, ExecutionStopped: true, Note: "Verified helper stopped and inspected actual cleanup"}
			if err := store.DisposeHelperExecution(ctx, id, "not-admin", decision); err == nil {
				t.Fatal("non-admin resolved execution")
			}
			unconfirmed := decision
			unconfirmed.ExecutionStopped = false
			if err := store.DisposeHelperExecution(ctx, id, adminID, unconfirmed); err == nil {
				t.Fatal("missing stopped confirmation accepted")
			}
			body, err := json.Marshal(decision)
			if err != nil {
				t.Fatal(err)
			}
			handler := NewServer(store, "", false).Handler()
			post := func(token string, want int) {
				r := httptest.NewRequest(http.MethodPost, "/api/v1/executions/"+id+"/resolve-helper", bytes.NewReader(body))
				r.AddCookie(&http.Cookie{Name: "vastora_session", Value: cookie})
				r.Header.Set("X-CSRF-Token", token)
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != want {
					t.Fatalf("disposition status=%d want=%d body=%s", w.Code, want, w.Body.String())
				}
			}
			post("", http.StatusUnauthorized)
			post(csrf, http.StatusOK)
			post(csrf, http.StatusConflict)
			var disposition, actor, state, business string
			var callbackHash []byte
			if err := store.db.QueryRow(`SELECT e.disposition,e.disposition_actor,e.state,d.state,d.callback_token_hash FROM task_executions e JOIN agent_decommissions d ON d.agent_id=e.agent_id WHERE e.id=?`, id).Scan(&disposition, &actor, &state, &business, &callbackHash); err != nil {
				t.Fatal(err)
			}
			want := "failed"
			if action == "confirm-completed" {
				want = "succeeded"
			}
			if disposition != action || actor != adminID || state != "unknown" || business != want || len(callbackHash) != 0 {
				t.Fatalf("invalid disposition: %s/%s/%s/%s hash=%d", disposition, actor, state, business, len(callbackHash))
			}
			if err := store.AuthorizeDecommissionStep(ctx, task.ID, task.DecommissionCallbackToken, task.Attempt, 1, "command"); err == nil {
				t.Fatal("old helper authority survived disposition")
			}
			if err := store.completeAgentDecommissionCallback(ctx, task.ID, task.DecommissionCallbackToken, task.Attempt, ""); err == nil {
				t.Fatal("old result survived disposition")
			}
		})
	}
}

func TestExecutionDecommissionExpiredHelperRequiresNewAttempt(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	configureDecommissionCallbackForTest(t, store)
	node := enrollOrchestrationNode(t, store, "expired-helper", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.22", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.22", LANAddress: "10.0.0.22", EnabledKinds: []string{networking.KindLAN}})
	if err := store.queueAgentDecommission(ctx, node.ID, true); err != nil {
		t.Fatal(err)
	}
	task := waitForDecommissionTask(t, store, node)
	id, session := authorizeDecommissionForTest(t, store, node, *task)
	if err := store.beginAgentDecommission(ctx, node.ID, node.Credential, task.ID, task.Attempt, id, session); err != nil {
		t.Fatal(err)
	}
	now := store.now().Add(31 * time.Minute)
	store.now = func() time.Time { return now }
	if err := store.executionClaimAllowed(ctx, node.ID, session); !errors.Is(err, errExecutionBlocked) {
		t.Fatal(err)
	}
	cookie, _, err := store.CreateFirstAdmin(ctx, "operator", "test-only-long-password")
	if err != nil {
		t.Fatal(err)
	}
	adminID, err := store.SessionAdminID(ctx, cookie)
	if err != nil {
		t.Fatal(err)
	}
	decision := controlplane.ExecutionDisposition{Action: "reexecute", ExecutionStopped: true, Note: "Confirmed expired helper stopped before new attempt"}
	if err := store.ReexecuteExecution(ctx, id, adminID, decision); err != nil {
		t.Fatal(err)
	}
	next, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil || next == nil || next.ID != task.ID || next.Attempt != task.Attempt+1 || next.DecommissionCallbackToken == task.DecommissionCallbackToken {
		t.Fatalf("new attempt not isolated: next=%v err=%v", next != nil, err)
	}
	if err := store.AuthorizeDecommissionStep(ctx, task.ID, task.DecommissionCallbackToken, task.Attempt, 1, "command"); err == nil {
		t.Fatal("expired helper retained authority")
	}
	if err := store.completeAgentDecommissionCallback(ctx, task.ID, task.DecommissionCallbackToken, task.Attempt, ""); err == nil {
		t.Fatal("old callback completed new attempt")
	}
}

func TestExecutionDecommissionHandoffFencesOldHelpers(t *testing.T) {
	for _, mode := range []string{"lease-conflict", "session-replaced", "expired", "disposed", "completed", "cleanup-failed"} {
		t.Run(mode, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			configureDecommissionCallbackForTest(t, store)
			node := enrollOrchestrationNode(t, store, "cleanup-test", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.19", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.19", LANAddress: "10.0.0.19", EnabledKinds: []string{networking.KindLAN}})
			if err := store.queueAgentDecommission(ctx, node.ID, true); err != nil {
				t.Fatal(err)
			}
			task := waitForDecommissionTask(t, store, node)
			id, session := authorizeDecommissionForTest(t, store, node, *task)
			begin := func(sid string) error {
				return store.beginAgentDecommission(ctx, node.ID, node.Credential, task.ID, task.Attempt, id, sid)
			}
			if err := begin("wrong-session"); !errors.Is(err, errExecutionAuthorization) {
				t.Fatalf("wrong session: %v", err)
			}
			if mode == "lease-conflict" {
				if _, err := store.db.Exec(`UPDATE agent_decommissions SET lease_expires_at='' WHERE agent_id=?`, node.ID); err != nil {
					t.Fatal(err)
				}
				if err := begin(session); !errors.Is(err, errStaleTaskLease) {
					t.Fatalf("lease conflict: %v", err)
				}
				var state, phase string
				if err := store.db.QueryRow(`SELECT state,phase FROM task_executions WHERE id=?`, id).Scan(&state, &phase); err != nil || state != "running" || phase != "handoff" {
					t.Fatalf("failed handoff consumed authorization: %s/%s %v", state, phase, err)
				}
				return
			}
			if err := begin(session); err != nil {
				t.Fatal(err)
			}
			if err := begin(session); !errors.Is(err, errExecutionAuthorization) {
				t.Fatalf("repeated handoff: %v", err)
			}
			if err := store.completeAgentDecommissionCallback(ctx, task.ID, task.DecommissionCallbackToken, task.Attempt, ""); err == nil {
				t.Fatal("success before final authorized stage")
			}
			if err := store.AuthorizeDecommissionStep(ctx, task.ID, "wrong-token", task.Attempt, 1, "command"); err == nil {
				t.Fatal("wrong step token accepted")
			}
			if err := store.AuthorizeDecommissionStep(ctx, task.ID, task.DecommissionCallbackToken, task.Attempt, 2, "files"); err == nil {
				t.Fatal("skipped sequence accepted")
			}
			for i, phase := range []string{"command", "runtime", "files", "done"} {
				if err := store.AuthorizeDecommissionStep(ctx, task.ID, task.DecommissionCallbackToken, task.Attempt, int64(i+1), phase); err != nil {
					t.Fatal(err)
				}
				if err := store.AuthorizeDecommissionStep(ctx, task.ID, task.DecommissionCallbackToken, task.Attempt, int64(i+1), phase); err == nil {
					t.Fatal("repeated step accepted")
				}
			}
			if err := store.AuthorizeDecommissionStep(ctx, task.ID, task.DecommissionCallbackToken, task.Attempt, 5, "files"); err == nil {
				t.Fatal("mutations after done accepted")
			}
			if mode == "session-replaced" {
				if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, "replacement-cleanup-session", controlplane.ExecutionProtocol); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "expired" {
				now := store.now().Add(31 * time.Minute)
				store.now = func() time.Time { return now }
			}
			if mode == "disposed" {
				// Emulate an already committed operator disposition. The callback
				// must remain fenced regardless of the independently held token.
				if _, err := store.db.Exec(`UPDATE task_executions SET state='unknown',disposition='abandon' WHERE id=?`, id); err != nil {
					t.Fatal(err)
				}
			}
			failure := ""
			if mode == "cleanup-failed" {
				failure = "cleanup interrupted after service stop"
			}
			err := store.completeAgentDecommissionCallback(ctx, task.ID, task.DecommissionCallbackToken, task.Attempt, failure)
			blocked := mode == "expired" || mode == "disposed"
			if (err != nil) != blocked {
				t.Fatalf("callback: %v", err)
			}
			var business, execution string
			if err := store.db.QueryRow(`SELECT d.state,e.state FROM agent_decommissions d JOIN task_executions e ON e.agent_id=d.agent_id WHERE e.id=?`, id).Scan(&business, &execution); err != nil {
				t.Fatal(err)
			}
			if blocked && business != "cleaning" {
				t.Fatalf("rejected result changed cleanup: %s", business)
			}
			if mode == "cleanup-failed" && (business != "failed" || execution != "unknown") {
				t.Fatalf("failed cleanup was not fenced: %s/%s", business, execution)
			}
			if !blocked && mode != "cleanup-failed" && (business != "succeeded" || execution != "succeeded") {
				t.Fatalf("non-atomic completion: %s/%s", business, execution)
			}
			if !blocked {
				var sealed []byte
				if err := store.db.QueryRow(`SELECT sealed_result FROM task_executions WHERE id=?`, id).Scan(&sealed); err != nil {
					t.Fatal(err)
				}
				raw, err := secret.Open(store.key, sealed, []byte("execution-result:"+id))
				if err != nil {
					t.Fatal(err)
				}
				var evidence struct{ Succeeded, Unknown bool }
				if err := json.Unmarshal(raw, &evidence); err != nil || evidence.Succeeded != (failure == "") || evidence.Unknown != (failure != "") {
					t.Fatalf("callback evidence: %+v %v", evidence, err)
				}
			}
			if err := store.completeAgentDecommissionCallback(ctx, task.ID, task.DecommissionCallbackToken, task.Attempt, ""); err == nil {
				t.Fatal("late or duplicate callback accepted")
			}
		})
	}
}
