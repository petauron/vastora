package center

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/petauron/vastora/internal/agentpb"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestExecutionGRPCAuthorizationAndStopFence(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "unknown"}[unknown], func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "execution-http", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.18", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.18", LANAddress: "10.0.0.18", EnabledKinds: []string{networking.KindLAN}})
			server := NewServer(store, "", false)
			handler := server.Handler()
			service := newAgentGRPCService(server)
			post := func(executionID, credential string, input any, want codes.Code) {
				t.Helper()
				body, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				rpcCtx := agentGRPCTestContext(ctx, credential)
				if executionID == "" {
					_, err = service.RegisterExecutionSession(rpcCtx, &agentpb.AgentDocument{AgentId: node.ID, Json: body})
				} else {
					_, err = service.TransitionExecution(rpcCtx, &agentpb.ExecutionDocument{AgentId: node.ID, ExecutionId: executionID, Json: body})
				}
				if status.Code(err) != want {
					t.Fatalf("execution %q: status %s, want %s: %v", executionID, status.Code(err), want, err)
				}
			}
			base := "/api/v1/agents/" + node.ID
			session := "execution-http-current-process-session"
			registration := controlplane.ExecutionSessionRequest{SessionID: session, Protocol: controlplane.ExecutionProtocol}
			post("", "", registration, codes.Unauthenticated)
			post("", "wrong-credential", registration, codes.Unauthenticated)
			post("", node.Credential, registration, codes.OK)
			for _, query := range []string{"recovery=%7B%7D", "taskId=previous-task", "wait=0s&wait=1s", "recovery="} {
				r := httptest.NewRequest(http.MethodGet, base+"/tasks/next?"+query, nil)
				r.Header.Set("Authorization", "Bearer "+node.Credential)
				r.Header.Set("X-Vastora-Execution-Session", session)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != http.StatusNotFound {
					t.Fatalf("retired or ambiguous claim %q returned %d", query, w.Code)
				}
			}
			auth, err := store.PersistExecutionAuthorization(ctx, node.ID, session, AgentTask{ID: "http-task", Kind: "application.apply", Attempt: 1})
			if err != nil {
				t.Fatal(err)
			}
			path := auth.ID
			start := controlplane.ExecutionTransitionRequest{SessionID: session, Action: "start", Digest: auth.Digest}
			post(path, "wrong-credential", start, codes.Unauthenticated)
			stale := start
			stale.SessionID = "execution-http-obsolete-process-session"
			post(path, node.Credential, stale, codes.FailedPrecondition)
			post(path, node.Credential, start, codes.OK)
			post(path, node.Credential, start, codes.FailedPrecondition)
			step := controlplane.ExecutionTransitionRequest{SessionID: session, Action: "step", Phase: "apply"}
			post(path, node.Credential, step, codes.OK)
			post(path, node.Credential, controlplane.ExecutionTransitionRequest{SessionID: session, Action: "renew"}, codes.OK)
			post(path, node.Credential, controlplane.ExecutionTransitionRequest{SessionID: session, Action: "stop", Unknown: unknown, Error: "operation interrupted"}, codes.OK)
			post(path, node.Credential, step, codes.FailedPrecondition)
			post(path, node.Credential, controlplane.ExecutionTransitionRequest{SessionID: session, Action: "renew"}, codes.FailedPrecondition)
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
			post("", node.Credential, replacement, codes.OK)
			// Retrying registration in the current process is harmless, but an
			// older process must never regain authority by reconnecting.
			post("", node.Credential, replacement, codes.OK)
			post("", node.Credential, registration, codes.FailedPrecondition)
			var current string
			if err := store.db.QueryRowContext(ctx, `SELECT session_id FROM agent_execution_sessions WHERE agent_id=?`, node.ID).Scan(&current); err != nil || current != replacement.SessionID {
				t.Fatalf("retired session replaced current process: %s %v", current, err)
			}
		})
	}
}
