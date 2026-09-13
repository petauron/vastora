package center

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
)

func TestExecutionHTTPAuthorizationAndStopFence(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "unknown"}[unknown], func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "execution-http", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.18", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.18", LANAddress: "10.0.0.18", EnabledKinds: []string{networking.KindLAN}})
			handler := NewServer(store, "", false).Handler()
			post := func(path, credential string, input any, status int) {
				t.Helper()
				body, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				if credential != "" {
					r.Header.Set("Authorization", "Bearer "+credential)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != status {
					t.Fatalf("%s: status %d, want %d: %s", path, w.Code, status, w.Body.String())
				}
			}
			base := "/api/v1/agents/" + node.ID
			session := "execution-http-current-process-session"
			registration := controlplane.ExecutionSessionRequest{SessionID: session, Protocol: controlplane.ExecutionProtocol}
			post(base+"/execution-session", "", registration, http.StatusUnauthorized)
			post(base+"/execution-session", "wrong-credential", registration, http.StatusForbidden)
			post(base+"/execution-session", node.Credential, registration, http.StatusOK)
			for _, query := range []string{"recovery=%7B%7D", "taskId=previous-task", "wait=0s&wait=1s", "recovery="} {
				r := httptest.NewRequest(http.MethodGet, base+"/tasks/next?"+query, nil)
				r.Header.Set("Authorization", "Bearer "+node.Credential)
				r.Header.Set("X-Vastora-Execution-Session", session)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != http.StatusBadRequest {
					t.Fatalf("retired or ambiguous claim %q returned %d", query, w.Code)
				}
			}
			auth, err := store.PersistExecutionAuthorization(ctx, node.ID, session, AgentTask{ID: "http-task", Kind: "application.apply", Attempt: 1})
			if err != nil {
				t.Fatal(err)
			}
			path := base + "/executions/" + auth.ID
			start := controlplane.ExecutionTransitionRequest{SessionID: session, Action: "start", Digest: auth.Digest}
			post(path, "wrong-credential", start, http.StatusUnauthorized)
			stale := start
			stale.SessionID = "execution-http-obsolete-process-session"
			post(path, node.Credential, stale, http.StatusConflict)
			post(path, node.Credential, start, http.StatusOK)
			post(path, node.Credential, start, http.StatusConflict)
			step := controlplane.ExecutionTransitionRequest{SessionID: session, Action: "step", Phase: "apply"}
			post(path, node.Credential, step, http.StatusOK)
			post(path, node.Credential, controlplane.ExecutionTransitionRequest{SessionID: session, Action: "renew"}, http.StatusOK)
			post(path, node.Credential, controlplane.ExecutionTransitionRequest{SessionID: session, Action: "stop", Unknown: unknown, Error: "operation interrupted"}, http.StatusOK)
			post(path, node.Credential, step, http.StatusConflict)
			post(path, node.Credential, controlplane.ExecutionTransitionRequest{SessionID: session, Action: "renew"}, http.StatusConflict)
			if err := store.executionClaimAllowed(ctx, node.ID, session); !errors.Is(err, errExecutionBlocked) {
				t.Fatalf("stop did not fence node: %v", err)
			}
			views, err := executionViewsForTest(ctx, store)
			if err != nil || len(views) != 1 {
				t.Fatalf("execution evidence: %v %v", views, err)
			}
			want := "failed"
			if unknown {
				want = "unknown"
			}
			if views[0].State != want || views[0].Phase != "apply" {
				t.Fatalf("lost stopped phase: %#v", views[0])
			}
			var events int
			if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_events WHERE execution_id=?`, auth.ID).Scan(&events); err != nil || events != 4 {
				t.Fatalf("audit events=%d: %v", events, err)
			}
			replacement := controlplane.ExecutionSessionRequest{SessionID: "execution-http-replacement-process-session", Protocol: controlplane.ExecutionProtocol}
			post(base+"/execution-session", node.Credential, replacement, http.StatusOK)
			// Retrying registration in the current process is harmless, but an
			// older process must never regain authority by reconnecting.
			post(base+"/execution-session", node.Credential, replacement, http.StatusOK)
			post(base+"/execution-session", node.Credential, registration, http.StatusForbidden)
			var current string
			if err := store.db.QueryRowContext(ctx, `SELECT session_id FROM agent_execution_sessions WHERE agent_id=?`, node.ID).Scan(&current); err != nil || current != replacement.SessionID {
				t.Fatalf("retired session replaced current process: %s %v", current, err)
			}
		})
	}
}
