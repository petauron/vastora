package center

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
)

func TestExecutionApplicationConfirmationUsesRetainedEvidenceAtomically(t *testing.T) {
	for _, mode := range []string{"success", "audit-failure", "commit-failure", "invalid-result", "missing-generation", "failed-result", "unknown-result", "not-admin", "not-stopped"} {
		t.Run(mode, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "confirmation", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.19", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.19", LANAddress: "10.0.0.19", EnabledKinds: []string{networking.KindLAN}})
			deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"debug":false}`)})
			if err != nil {
				t.Fatal(err)
			}
			session := "confirmation-original-process-session"
			if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
				t.Fatal(err)
			}
			task, err := store.claimExecutionTask(ctx, node.ID, node.Credential, session, 0)
			if err != nil || task == nil {
				t.Fatalf("claim: %v", err)
			}
			if err := store.StartExecution(ctx, node.ID, session, task.Authorization.ID, task.Authorization.Digest); err != nil {
				t.Fatal(err)
			}
			result := cpaApplicationResult("10.0.0.19")
			if mode == "invalid-result" {
				result = json.RawMessage(`{"generatedSecrets":{"management_key":"retained-test-secret"}}`)
			}
			generation := &task.RequiredRuntimeGeneration
			if mode == "missing-generation" {
				generation = nil
			}
			if err := store.StoreExecutionResult(ctx, node.ID, session, task.Authorization.ID, result, mode != "failed-result", mode == "unknown-result", "", generation); err != nil {
				t.Fatal(err)
			}
			// Model an older unresolved execution so the administrator recovery
			// path remains covered independently from automatic result recovery.
			if _, err := store.db.Exec(`UPDATE task_executions SET state='unknown',last_error='legacy interrupted execution' WHERE id=?`, task.Authorization.ID); err != nil {
				t.Fatal(err)
			}
			cookie, csrf, err := store.CreateFirstAdmin(ctx, "confirmation-admin", "test-only-strong-password")
			if err != nil {
				t.Fatal(err)
			}
			adminID, err := store.SessionAdminID(ctx, cookie)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "not-admin" {
				adminID = "missing-admin"
			}
			input := controlplane.ExecutionDisposition{Action: "confirm-completed", ExecutionStopped: mode != "not-stopped", Note: "Verified the previous process stopped and the application matches its retained result."}
			if mode == "audit-failure" {
				if _, err := store.db.Exec(`CREATE TRIGGER reject_confirmation BEFORE UPDATE ON task_executions WHEN NEW.disposition='confirm-completed' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "commit-failure" {
				for _, statement := range []string{
					`CREATE TABLE confirmation_parent(id INTEGER PRIMARY KEY)`,
					`CREATE TABLE confirmation_child(parent INTEGER REFERENCES confirmation_parent(id) DEFERRABLE INITIALLY DEFERRED)`,
					`CREATE TRIGGER reject_confirmation AFTER UPDATE ON task_executions WHEN NEW.disposition='confirm-completed' BEGIN INSERT INTO confirmation_child(parent) VALUES(1); END`,
				} {
					if _, err := store.db.Exec(statement); err != nil {
						t.Fatal(err)
					}
				}
			}
			var before []byte
			if err := store.db.QueryRow(`SELECT sealed_result FROM task_executions WHERE id=?`, task.Authorization.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if mode == "success" {
				page, err := store.ListExecutions(ctx, 0)
				if err != nil {
					t.Fatal(err)
				}
				confirmable := false
				for _, view := range page.Executions {
					if view.ID == task.Authorization.ID {
						confirmable = view.CanConfirm
					}
				}
				if !confirmable {
					t.Fatal("retained successful result was not offered for confirmation")
				}
				payload, marshalErr := json.Marshal(input)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				handler := NewServer(store, "", false).Handler()
				for _, auth := range []string{"anonymous", "missing-csrf", "admin", "duplicate"} {
					request := httptest.NewRequest(http.MethodPost, "/api/v1/executions/"+task.Authorization.ID+"/confirm-completed", bytes.NewReader(payload))
					request.Header.Set("Content-Type", "application/json")
					if auth != "anonymous" {
						request.AddCookie(&http.Cookie{Name: "vastora_session", Value: cookie})
					}
					if auth == "admin" || auth == "duplicate" {
						request.Header.Set("X-CSRF-Token", csrf)
					}
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, request)
					want := map[string]int{"anonymous": 401, "missing-csrf": 401, "admin": 200, "duplicate": 409}[auth]
					if response.Code != want {
						t.Fatalf("confirmation %s: %d want %d", auth, response.Code, want)
					}
				}
				err = nil
			} else {
				err = store.ConfirmExecution(ctx, task.Authorization.ID, adminID, input)
			}
			if (err == nil) != (mode == "success") {
				t.Fatalf("confirmation mode %s: %v", mode, err)
			}
			var state, disposition, actor, deploymentState string
			var after []byte
			var attempt int64
			var services, executions int
			if err := store.db.QueryRow(`SELECT state,disposition,disposition_actor,sealed_result FROM task_executions WHERE id=?`, task.Authorization.ID).Scan(&state, &disposition, &actor, &after); err != nil {
				t.Fatal(err)
			}
			if err := store.db.QueryRow(`SELECT state,attempt FROM deployments WHERE id=?`, task.ID).Scan(&deploymentState, &attempt); err != nil {
				t.Fatal(err)
			}
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM services WHERE application_id=?`, deployment.ApplicationID).Scan(&services); err != nil {
				t.Fatal(err)
			}
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_executions WHERE agent_id=?`, node.ID).Scan(&executions); err != nil {
				t.Fatal(err)
			}
			if state != "unknown" || !bytes.Equal(before, after) || attempt != task.Attempt || executions != 1 {
				t.Fatal("confirmation changed execution history, result evidence, or authorization")
			}
			if mode == "success" {
				if disposition != "confirm-completed" || actor != adminID || deploymentState != "succeeded" || services == 0 {
					t.Fatal("confirmation did not commit matching business state and disposition")
				}
				if err := store.ConfirmExecution(ctx, task.Authorization.ID, adminID, input); err == nil {
					t.Fatal("confirmation could be replayed")
				}
			} else if disposition != "" || deploymentState != "running" || services != 0 {
				t.Fatal("failed confirmation left partial business changes")
			}
		})
	}
}

func TestExecutionSessionRecoversRetainedSuccessfulResult(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "automatic-result-recovery", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.19", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.19", LANAddress: "10.0.0.19", EnabledKinds: []string{networking.KindLAN}})
	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"debug":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	session := "automatic-result-original-session"
	if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	task, err := store.claimExecutionTask(ctx, node.ID, node.Credential, session, 0)
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.StartExecution(ctx, node.ID, session, task.Authorization.ID, task.Authorization.Digest); err != nil {
		t.Fatal(err)
	}
	generation := task.RequiredRuntimeGeneration
	if err := store.StoreExecutionResult(ctx, node.ID, session, task.Authorization.ID, cpaApplicationResult("10.0.0.19"), true, false, "", &generation); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, "automatic-result-replacement-session", controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	var executionState, phase, disposition, deploymentState string
	var services int
	if err := store.db.QueryRow(`SELECT state,phase,disposition FROM task_executions WHERE id=?`, task.Authorization.ID).Scan(&executionState, &phase, &disposition); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT state FROM deployments WHERE id=?`, task.ID).Scan(&deploymentState); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM services WHERE application_id=?`, deployment.ApplicationID).Scan(&services); err != nil {
		t.Fatal(err)
	}
	if executionState != "succeeded" || phase != "reported" || disposition != "" || deploymentState != "succeeded" || services == 0 {
		t.Fatalf("retained result was not recovered: execution=%s phase=%s disposition=%q deployment=%s services=%d", executionState, phase, disposition, deploymentState, services)
	}
}
