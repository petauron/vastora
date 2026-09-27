package center

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
)

type executionFlowExecutor struct {
	calls      atomic.Int64
	fail       bool
	restored   atomic.Int64
	maintained atomic.Int64
}

func (e *executionFlowExecutor) Restore(context.Context, *agent.Store) error {
	e.restored.Add(1)
	return errors.New("offline restoration must not run")
}

func (e *executionFlowExecutor) Maintain(context.Context) error {
	e.maintained.Add(1)
	return errors.New("periodic mutation must not run")
}

func (e *executionFlowExecutor) Deploy(_ context.Context, task agent.DeploymentTask) (agent.ApplicationTaskResult, error) {
	e.calls.Add(1)
	if e.fail {
		return agent.ApplicationTaskResult{}, errors.New("simulated operation failure")
	}
	var result agent.ApplicationTaskResult
	if err := json.Unmarshal(cpaApplicationResult("10.0.0.19"), &result); err != nil {
		return result, err
	}
	// The simulated Docker effect still returns the exact admitted package
	// identity; execution projection no longer accepts service-only success.
	result.Resources = &agent.InstanceResources{
		Version: 1, ApplicationID: task.ApplicationID, AppKey: task.AppKey,
		Runtime: task.Manifest.Runtime.Kind, PackageVersion: task.Manifest.Version,
		PackageRevision: task.PackageRevision, ManifestSHA256: task.ManifestSHA256,
		AuthorizedCapabilities: task.AuthorizedCapabilities, State: "ready", TaskID: task.ID,
		Resources: []agent.RuntimeResource{{Kind: "container", LogicalName: "cpa", Name: "isolated-flow-cpa", ID: "fixture-container-id"}},
	}
	return result, nil
}

// Exercises the actual Agent loop, encrypted claim, one-use start, execution
// phases and business result projection. Only the external Docker effect is fake.
func TestExecutionAgentCenterFlow(t *testing.T) {
	for _, mode := range []string{"success", "failure", "abandon", "result-lost", "claim-lost", "start-lost"} {
		t.Run(mode, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "execution-flow", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.19", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.19", LANAddress: "10.0.0.19", EnabledKinds: []string{networking.KindLAN}})
			private, public, err := controlplane.GenerateKeyPair()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE agents SET x25519_public_key=? WHERE id=?`, public, node.ID); err != nil {
				t.Fatal(err)
			}
			handler := NewServer(store, "", false).Handler()
			var reports atomic.Int64
			var lost atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "claim-lost" && strings.HasSuffix(r.URL.Path, "/tasks/next") && !lost.Load() || mode == "start-lost" && strings.Contains(r.URL.Path, "/executions/") && !lost.Load() {
					// Let Center commit the grant/consumption, then lose its reply.
					recorder := httptest.NewRecorder()
					handler.ServeHTTP(recorder, r)
					if recorder.Code != http.StatusOK {
						w.WriteHeader(recorder.Code)
						return
					}
					lost.Store(true)
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/result") {
					reports.Add(1)
					if mode == "result-lost" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			local, err := agent.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer local.Close()
			if err := local.SaveConnection(ctx, agent.Connection{AgentID: node.ID, Name: "execution-flow", CenterURL: server.URL, Credential: node.Credential, PrivateKey: private}); err != nil {
				t.Fatal(err)
			}
			deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: "vastora-official/cpa", Config: json.RawMessage(`{"debug":false}`), AuthorizedCapabilities: testCapabilityGrant("root")})
			if err != nil {
				t.Fatal(err)
			}
			executor := &executionFlowExecutor{fail: mode == "failure" || mode == "abandon"}
			client := agent.Client{HTTPClient: server.Client(), Executor: executor, Capabilities: agent.Capabilities{Docker: true, ExecutorVersions: map[string]int{"docker": 1, "systemd": 1}, RuntimeCapabilities: []string{"root"}}}
			defer func() {
				if executor.restored.Load() != 0 || executor.maintained.Load() != 0 {
					t.Fatalf("execution loop bypassed authorization: restore=%d maintenance=%d", executor.restored.Load(), executor.maintained.Load())
				}
			}()
			run, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			errorLog := make(chan error, 32)
			go func() {
				defer close(done)
				client.RunTasks(run, local, func(err error) {
					select {
					case errorLog <- err:
					default:
					}
				})
			}()
			defer func() { cancel(); <-done }()
			deadline := time.Now().Add(5 * time.Second)
			var views []ExecutionView
			for time.Now().Before(deadline) {
				views, err = executionViewsForTest(ctx, store)
				if err != nil {
					t.Fatal(err)
				}
				if len(views) == 1 && reports.Load() == 1 && (mode == "result-lost" || views[0].State == "succeeded" || views[0].State == "failed") {
					break
				}
				if len(views) == 1 && lost.Load() {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			wantEffects := int64(1)
			if mode == "claim-lost" || mode == "start-lost" {
				wantEffects = 0
			}
			if len(views) != 1 || reports.Load() != wantEffects || executor.calls.Load() != wantEffects {
				select {
				case err := <-errorLog:
					t.Log(err)
				default:
				}
				t.Fatalf("execution flow did not complete: views=%v reports=%d effects=%d", views, reports.Load(), executor.calls.Load())
			}
			if views[0].TaskID != deployment.ID {
				t.Fatalf("wrong task: %#v", views[0])
			}
			want := map[string]string{"success": "succeeded", "failure": "failed", "abandon": "failed", "result-lost": "running", "claim-lost": "offered", "start-lost": "running"}[mode]
			if views[0].State != want {
				t.Fatalf("state=%s want=%s", views[0].State, want)
			}
			if mode == "abandon" {
				old := views[0]
				cookie, csrf, err := store.CreateFirstAdmin(ctx, "execution-admin", "test-only-strong-password")
				if err != nil {
					t.Fatal(err)
				}
				adminID, err := store.SessionAdminID(ctx, cookie)
				if err != nil {
					t.Fatal(err)
				}
				decisionInput := controlplane.ExecutionDisposition{Action: "abandon", ExecutionStopped: true, Note: "Verified executor stopped"}
				if err := store.AbandonExecution(ctx, old.ID, "not-admin", decisionInput); err == nil {
					t.Fatal("non-admin abandoned execution")
				}
				decisionInput.ExecutionStopped = false
				if err := store.AbandonExecution(ctx, old.ID, adminID, decisionInput); err == nil {
					t.Fatal("abandoned without stopped confirmation")
				}
				for _, auth := range []string{"anonymous", "missing-csrf", "admin", "duplicate"} {
					r := httptest.NewRequest(http.MethodPost, "/api/v1/executions/"+old.ID+"/abandon", strings.NewReader(`{"action":"abandon","executionStopped":true,"note":"Verified executor stopped; preserve existing effects"}`))
					r.Header.Set("Content-Type", "application/json")
					if auth != "anonymous" {
						r.AddCookie(&http.Cookie{Name: "vastora_session", Value: cookie})
					}
					if auth == "admin" || auth == "duplicate" {
						r.Header.Set("X-CSRF-Token", csrf)
					}
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, r)
					want := map[string]int{"anonymous": 401, "missing-csrf": 401, "admin": 200, "duplicate": 409}[auth]
					if w.Code != want {
						t.Fatalf("abandon %s: %d want %d", auth, w.Code, want)
					}
				}
				var state, decision, actor string
				var attempt int64
				if err := store.db.QueryRow(`SELECT state,attempt FROM deployments WHERE id=?`, old.TaskID).Scan(&state, &attempt); err != nil || state != "failed" || attempt != old.Attempt {
					t.Fatalf("abandon changed task attempt: %s %d %v", state, attempt, err)
				}
				if err := store.db.QueryRow(`SELECT disposition,disposition_actor FROM task_executions WHERE id=?`, old.ID).Scan(&decision, &actor); err != nil || decision != "abandon" || actor == "" {
					t.Fatalf("missing disposition audit: %s %s %v", decision, actor, err)
				}
				if task, err := store.ClaimNextTask(ctx, node.ID, node.Credential); err != nil || task != nil {
					t.Fatalf("abandoned operation claimed again: %+v %v", task, err)
				}
				if err := store.CompleteTask(ctx, node.ID, node.Credential, old.TaskID, old.Attempt, true, "", nil, 0); err == nil {
					t.Fatal("late result changed abandoned operation")
				}
			}
			if mode == "failure" {
				old := views[0]
				if _, err := store.RetryTaskReconciliation(ctx, old.TaskID); err == nil {
					t.Fatal("legacy retry bypassed explicit execution disposition")
				}
				session, csrf, err := store.CreateFirstAdmin(ctx, "execution-admin", "test-only-strong-password")
				if err != nil {
					t.Fatal(err)
				}
				adminID, err := store.SessionAdminID(ctx, session)
				if err != nil {
					t.Fatal(err)
				}
				decision := controlplane.ExecutionDisposition{Action: "reexecute", ExecutionStopped: true, Note: "Verified simulated executor returned and stopped"}
				unconfirmed := decision
				unconfirmed.ExecutionStopped = false
				if err := store.ReexecuteExecution(ctx, old.ID, adminID, unconfirmed); err == nil {
					t.Fatal("reexecuted without stopped confirmation")
				}
				if err := store.ReexecuteExecution(ctx, old.ID, "not-admin", decision); err == nil {
					t.Fatal("non-admin released execution fence")
				}
				encoded, err := json.Marshal(decision)
				if err != nil {
					t.Fatal(err)
				}
				for _, auth := range []string{"anonymous", "missing-csrf", "admin"} {
					r := httptest.NewRequest(http.MethodPost, "/api/v1/executions/"+old.ID+"/reexecute", strings.NewReader(string(encoded)))
					r.Header.Set("Content-Type", "application/json")
					if auth != "anonymous" {
						r.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
					}
					if auth == "admin" {
						r.Header.Set("X-CSRF-Token", csrf)
					}
					w := httptest.NewRecorder()
					handler.ServeHTTP(w, r)
					want := map[string]int{"anonymous": http.StatusUnauthorized, "missing-csrf": http.StatusUnauthorized, "admin": http.StatusAccepted}[auth]
					if w.Code != want {
						t.Fatalf("disposition auth %s: %d want %d: %s", auth, w.Code, want, w.Body.String())
					}
				}
				if err := store.ReexecuteExecution(ctx, old.ID, adminID, decision); err == nil {
					t.Fatal("same decision queued twice")
				}
				deadline = time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					views, err = executionViewsForTest(ctx, store)
					if err != nil {
						t.Fatal(err)
					}
					if len(views) == 2 && views[0].State == "failed" {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				if len(views) != 2 || views[0].ID == old.ID || views[0].Attempt != 2 || views[1].Disposition != "reexecute" || executor.calls.Load() != 2 {
					t.Fatalf("manual decision did not create isolated attempt: %v calls=%d", views, executor.calls.Load())
				}
				var actor string
				if err := store.db.QueryRowContext(ctx, `SELECT disposition_actor FROM task_executions WHERE id=?`, old.ID).Scan(&actor); err != nil || actor != adminID {
					t.Fatalf("missing operator audit: %s %v", actor, err)
				}
			}
			// The ordinary execution must not leave a local persistent receipt.
			if pending, _, err := local.NextLegacyReceipt(ctx, ""); err != nil || pending != nil {
				t.Fatalf("local completion queue survived: %v %v", pending, err)
			}
			if mode == "result-lost" || mode == "claim-lost" || mode == "start-lost" {
				cancel()
				<-done
				restart, stopRestart := context.WithCancel(ctx)
				restarted := make(chan struct{})
				go func() { defer close(restarted); client.RunTasks(restart, local, nil) }()
				defer func() { stopRestart(); <-restarted }()
				deadline = time.Now().Add(2 * time.Second)
				for time.Now().Before(deadline) {
					views, err = executionViewsForTest(ctx, store)
					if err != nil {
						t.Fatal(err)
					}
					if len(views) == 1 && views[0].State == "unknown" {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				if len(views) != 1 || views[0].State != "unknown" || executor.calls.Load() != wantEffects {
					t.Fatalf("restart replayed uncertain operation: %v calls=%d", views, executor.calls.Load())
				}
			}
		})
	}
}
