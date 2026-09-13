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
	"github.com/petauron/vastora/internal/secret"
)

func TestExecutionProjectionAndOutcomeCommitAtomically(t *testing.T) {
	for _, failure := range []string{"none", "finalize", "commit", "service-write", "invalid-result", "missing-runtime-generation"} {
		t.Run(failure, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "atomic-result", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.19", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.19", LANAddress: "10.0.0.19", EnabledKinds: []string{networking.KindLAN}})
			created, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"debug":false}`)})
			if err != nil {
				t.Fatal(err)
			}
			session := "atomic-result-current-process-session"
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
			var originalApp string
			if err := store.db.QueryRow(`SELECT status FROM applications WHERE id=?`, created.ApplicationID).Scan(&originalApp); err != nil {
				t.Fatal(err)
			}
			statements := []string{}
			switch failure {
			case "finalize":
				statements = append(statements, `CREATE TRIGGER fail_projection BEFORE UPDATE ON task_executions WHEN NEW.phase='reported' BEGIN SELECT RAISE(ABORT,'finalization unavailable'); END`)
			case "commit":
				statements = append(statements, `CREATE TABLE result_parent(id INTEGER PRIMARY KEY)`, `CREATE TABLE result_child(parent INTEGER REFERENCES result_parent(id) DEFERRABLE INITIALLY DEFERRED)`, `CREATE TRIGGER fail_projection AFTER UPDATE ON task_executions WHEN NEW.phase='reported' BEGIN INSERT INTO result_child(parent) VALUES(1); END`)
			case "service-write":
				statements = append(statements, `CREATE TRIGGER fail_projection BEFORE INSERT ON services BEGIN SELECT RAISE(ABORT,'service write unavailable'); END`)
			}
			for _, statement := range statements {
				if _, err := store.db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			result := cpaApplicationResult("10.0.0.19")
			if failure == "invalid-result" {
				result = json.RawMessage(`{"generatedSecrets":{"management_key":"retained-test-secret"}}`)
			}
			input := map[string]any{"executionId": task.Authorization.ID, "sessionId": session, "attempt": task.Attempt, "succeeded": true, "result": json.RawMessage(result), "applicationRuntimeGeneration": task.RequiredRuntimeGeneration}
			if failure == "missing-runtime-generation" {
				delete(input, "applicationRuntimeGeneration")
			}
			payload, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			handler := NewServer(store, "", false).Handler()
			send := func() int {
				request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+node.ID+"/tasks/"+task.ID+"/result", bytes.NewReader(payload))
				request.Header.Set("Authorization", "Bearer "+node.Credential)
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				return response.Code
			}
			status := send()
			if (status == 200) != (failure == "none") {
				t.Fatalf("response %d for %s", status, failure)
			}
			var deploymentState, appState, executionState, phase string
			var resultSize, services, reported int
			if err := store.db.QueryRow(`SELECT state FROM deployments WHERE id=?`, task.ID).Scan(&deploymentState); err != nil {
				t.Fatal(err)
			}
			if err := store.db.QueryRow(`SELECT status FROM applications WHERE id=?`, created.ApplicationID).Scan(&appState); err != nil {
				t.Fatal(err)
			}
			if err := store.db.QueryRow(`SELECT state,phase,length(sealed_result) FROM task_executions WHERE id=?`, task.Authorization.ID).Scan(&executionState, &phase, &resultSize); err != nil {
				t.Fatal(err)
			}
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM services WHERE application_id=?`, created.ApplicationID).Scan(&services); err != nil {
				t.Fatal(err)
			}
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM execution_events WHERE execution_id=? AND phase='reported'`, task.Authorization.ID).Scan(&reported); err != nil {
				t.Fatal(err)
			}
			if resultSize == 0 {
				t.Fatal("lost result evidence")
			}
			var sealed []byte
			if err := store.db.QueryRow(`SELECT sealed_result FROM task_executions WHERE id=?`, task.Authorization.ID).Scan(&sealed); err != nil {
				t.Fatal(err)
			}
			raw, err := secret.Open(store.key, sealed, []byte("execution-result:"+task.Authorization.ID))
			if err != nil {
				t.Fatal(err)
			}
			var evidence executionResultEvidence
			if err := json.Unmarshal(raw, &evidence); err != nil {
				t.Fatal(err)
			}
			if failure == "missing-runtime-generation" {
				if evidence.ApplicationRuntimeGeneration != nil {
					t.Fatal("missing runtime generation was fabricated")
				}
			} else if evidence.ApplicationRuntimeGeneration == nil || *evidence.ApplicationRuntimeGeneration != task.RequiredRuntimeGeneration {
				t.Fatal("original runtime generation was not retained")
			}
			if !evidence.Succeeded || evidence.Unknown || !bytes.Equal(evidence.Result, result) {
				t.Fatal("original projection inputs were not retained")
			}
			if failure == "none" {
				if deploymentState != "succeeded" || appState != "running" || executionState != "succeeded" || phase != "reported" || reported != 1 || services == 0 {
					t.Fatalf("incomplete success: %s %s %s %s %d %d", deploymentState, appState, executionState, phase, reported, services)
				}
			} else {
				if deploymentState != "running" || appState != originalApp || executionState != "running" || phase != "result_received" || reported != 0 || services != 0 {
					t.Fatalf("partial projection survived: %s %s %s %s %d %d", deploymentState, appState, executionState, phase, reported, services)
				}
				if next, err := store.claimExecutionTask(ctx, node.ID, node.Credential, session, 0); err == nil || next != nil {
					t.Fatal("uncertain projection released execution fence")
				}
			}
			if send() == 200 {
				t.Fatal("duplicate result replayed projection")
			}
		})
	}
}
