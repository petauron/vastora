package center

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
)

func authorizeDecommissionForTest(t *testing.T, store *Store, node AgentCredential, task AgentTask) (string, string) {
	t.Helper()
	ctx := context.Background()
	session := "decommission-test-process-session"
	if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	auth, err := store.PersistExecutionAuthorization(ctx, node.ID, session, task)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StartExecution(ctx, node.ID, session, auth.ID, auth.Digest); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckExecutionStep(ctx, node.ID, session, auth.ID, "handoff"); err != nil {
		t.Fatal(err)
	}
	return auth.ID, session
}

func TestDecommissionApplicationsUsesNormalAgentLifecycle(t *testing.T) {
	for _, deleteData := range []bool{false, true} {
		t.Run(map[bool]string{false: "keep-data", true: "delete-data"}[deleteData], func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			configureDecommissionCallbackForTest(t, store)
			node := enrollOrchestrationNode(t, store, "decommission-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.90", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.90", LANAddress: "10.0.0.90", EnabledKinds: []string{networking.KindLAN}})
			installCPA(t, store, node, "10.0.0.90")

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			finished := make(chan error, 1)
			messages := make(chan string, 10)
			go func() {
				finished <- store.DecommissionApplications(ctx, deleteData, false, "", func(message string) { messages <- message })
			}()

			task := waitForDecommissionTask(t, store, node)
			if task.Operation != "uninstall" || task.AppKey != cpaAppKey || task.DeleteData != deleteData {
				t.Fatalf("unexpected decommission task: %#v", task)
			}
			if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil, task.RequiredRuntimeGeneration); err != nil {
				t.Fatal(err)
			}
			hostTask := waitForDecommissionTask(t, store, node)
			if hostTask.Kind != "agent.decommission" || hostTask.DeleteData != deleteData || hostTask.DecommissionCallbackURL == "" || hostTask.DecommissionCallbackToken == "" {
				t.Fatal("Agent host cleanup task is missing its callback binding")
			}
			executionID, sessionID := authorizeDecommissionForTest(t, store, node, *hostTask)
			if err := store.beginAgentDecommission(ctx, node.ID, node.Credential, hostTask.ID, hostTask.Attempt, executionID, sessionID); err != nil {
				t.Fatal(err)
			}
			if err := store.AuthorizeDecommissionStep(ctx, hostTask.ID, hostTask.DecommissionCallbackToken, hostTask.Attempt, 1, "done"); err != nil {
				t.Fatal(err)
			}
			if err := store.completeAgentDecommissionCallback(ctx, hostTask.ID, hostTask.DecommissionCallbackToken, hostTask.Attempt, ""); err != nil {
				t.Fatal(err)
			}
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			close(messages)
			var output strings.Builder
			for message := range messages {
				output.WriteString(message)
				output.WriteByte('\n')
			}
			if !strings.Contains(output.String(), "Managed applications to uninstall: 1") || !strings.Contains(output.String(), "Removed: CPA") {
				t.Fatalf("unexpected decommission progress: %q", output.String())
			}
			applications, err := store.ListApplications(ctx)
			if err != nil || len(applications) != 1 || applications[0].Status != "stopped" {
				t.Fatalf("application was not stopped: %#v err=%v", applications, err)
			}
		})
	}
}

func TestAgentDecommissionRequiresDurableCleanupHandoff(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	configureBuiltinHeadscaleForTest(t, store)
	node := enrollOrchestrationNode(t, store, "durable-cleanup-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.93", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.93", LANAddress: "10.0.0.93", EnabledKinds: []string{networking.KindLAN}})
	if err := store.queueAgentDecommission(context.Background(), node.ID, true); err != nil {
		t.Fatal(err)
	}
	task := waitForDecommissionTask(t, store, node)
	if !strings.HasPrefix(task.DecommissionCallbackURL, "https://headscale.example.test/api/v1/agent-decommission-results/") || task.DecommissionCallbackToken == "" {
		t.Fatal("cleanup task did not use the public Agent bootstrap origin")
	}
	if err := store.CompleteTask(context.Background(), node.ID, node.Credential, task.ID, task.Attempt, true, "", nil, 0); err == nil {
		t.Fatal("scheduled cleanup was accepted as completed before helper handoff")
	}
	executionID, sessionID := authorizeDecommissionForTest(t, store, node, *task)
	if err := store.beginAgentDecommission(context.Background(), node.ID, node.Credential, task.ID, task.Attempt, executionID, sessionID); err != nil {
		t.Fatal(err)
	}
	if err := store.beginAgentDecommission(context.Background(), node.ID, node.Credential, task.ID, task.Attempt, executionID, sessionID); !errors.Is(err, errExecutionAuthorization) {
		t.Fatalf("duplicate helper handoff was accepted: %v", err)
	}
	var state, lease string
	if err := store.db.QueryRow(`SELECT state, lease_expires_at FROM agent_decommissions WHERE agent_id = ?`, node.ID).Scan(&state, &lease); err != nil {
		t.Fatal(err)
	}
	if state != "cleaning" || lease != "" {
		t.Fatalf("durable cleanup state = %q lease=%q", state, lease)
	}
	if err := store.AuthorizeDecommissionStep(context.Background(), task.ID, task.DecommissionCallbackToken, task.Attempt, 1, "done"); err != nil {
		t.Fatal(err)
	}
	if err := store.completeAgentDecommissionCallback(context.Background(), task.ID, "wrong-token", task.Attempt, ""); err == nil {
		t.Fatal("invalid callback token was accepted")
	}
	request := httptest.NewRequest(http.MethodPost, task.DecommissionCallbackURL, strings.NewReader(fmt.Sprintf(`{"attempt":%d}`, task.Attempt)))
	request.Header.Set("Authorization", "Bearer "+task.DecommissionCallbackToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewServer(store, "", false).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("public cleanup callback status=%d body=%s", response.Code, response.Body.String())
	}
	if err := store.completeAgentDecommissionCallback(context.Background(), task.ID, task.DecommissionCallbackToken, task.Attempt, ""); err == nil {
		t.Fatal("duplicate final callback was accepted")
	}
}

func TestAgentDecommissionScheduleFailureRotatesCallback(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	configureDecommissionCallbackForTest(t, store)
	node := enrollOrchestrationNode(t, store, "cleanup-retry-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.94", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.94", LANAddress: "10.0.0.94", EnabledKinds: []string{networking.KindLAN}})
	if err := store.queueAgentDecommission(context.Background(), node.ID, true); err != nil {
		t.Fatal(err)
	}
	first := waitForDecommissionTask(t, store, node)
	executionID, sessionID := authorizeDecommissionForTest(t, store, node, *first)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+node.ID+"/tasks/"+first.ID+"/result", strings.NewReader(fmt.Sprintf(`{"attempt":%d,"executionId":%q,"sessionId":%q,"succeeded":false,"error":"persistent helper could not start","result":{}}`, first.Attempt, executionID, sessionID)))
	request.Header.Set("Authorization", "Bearer "+node.Credential)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewServer(store, "", false).Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("schedule failure result rejected: %d", response.Code)
	}
	if err := store.completeAgentDecommissionCallback(context.Background(), first.ID, first.DecommissionCallbackToken, first.Attempt, ""); err == nil {
		t.Fatal("failed claim retained an active callback token")
	}
	if next, err := store.ClaimNextTask(context.Background(), node.ID, node.Credential); !errors.Is(err, errExecutionBlocked) || next != nil {
		t.Fatalf("failed helper automatically retried: %v", err)
	}
	cookie, _, err := store.CreateFirstAdmin(context.Background(), "operator", "test-only-strong-password")
	if err != nil {
		t.Fatal(err)
	}
	adminID, err := store.SessionAdminID(context.Background(), cookie)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReexecuteExecution(context.Background(), executionID, adminID, controlplane.ExecutionDisposition{Action: "reexecute", ExecutionStopped: true, Note: "Verified helper never started; explicitly retry"}); err != nil {
		t.Fatal(err)
	}
	second := waitForDecommissionTask(t, store, node)
	if second.Attempt != first.Attempt+1 || second.DecommissionCallbackToken == first.DecommissionCallbackToken {
		t.Fatalf("cleanup retry did not rotate its task-bound callback: first attempt=%d second attempt=%d", first.Attempt, second.Attempt)
	}
}

func configureDecommissionCallbackForTest(t *testing.T, store *Store) {
	t.Helper()
	if _, err := store.db.ExecContext(context.Background(), `INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, agentConnectURLSetting, "https://center.example.test"); err != nil {
		t.Fatal(err)
	}
}

func TestDecommissionPriorityPreservesApplicationDependencies(t *testing.T) {
	tests := []struct {
		application ApplicationView
		want        int
	}{
		{ApplicationView{AppKey: threeXUIAppKey, Role: threeXUIRoleWorker}, 0},
		{ApplicationView{AppKey: "vastora-official/keeper"}, 0},
		{ApplicationView{AppKey: cpaAppKey}, 1},
		{ApplicationView{AppKey: threeXUIAppKey, Role: threeXUIRoleMaster}, 2},
	}
	for _, test := range tests {
		if got := decommissionPriority(test.application); got != test.want {
			t.Fatalf("priority for %#v = %d, want %d", test.application, got, test.want)
		}
	}
}

func TestDecommissionApplicationsFailsBeforeMutationWhenNodeIsOffline(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	node := enrollOrchestrationNode(t, store, "offline-decommission-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.91", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.91", LANAddress: "10.0.0.91", EnabledKinds: []string{networking.KindLAN}})
	installCPA(t, store, node, "10.0.0.91")
	if _, err := store.db.ExecContext(context.Background(), `UPDATE agents SET last_seen_at = ? WHERE id = ?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), node.ID); err != nil {
		t.Fatal(err)
	}
	err := store.DecommissionApplications(context.Background(), false, false, "", nil)
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("offline node did not block decommission: %v", err)
	}
	var uninstallTasks int
	if err := store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM deployments WHERE operation = 'uninstall'`).Scan(&uninstallTasks); err != nil {
		t.Fatal(err)
	}
	if uninstallTasks != 0 {
		t.Fatalf("offline preflight queued %d uninstall tasks", uninstallTasks)
	}
}

func TestForcedDecommissionReportsOfflineAgentWithoutClaimingCleanup(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	node := enrollOrchestrationNode(t, store, "offline-force-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.92", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.92", LANAddress: "10.0.0.92", EnabledKinds: []string{networking.KindLAN}})
	installCPA(t, store, node, "10.0.0.92")
	if _, err := store.db.ExecContext(context.Background(), `UPDATE agents SET last_seen_at = ? WHERE id = ?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), node.ID); err != nil {
		t.Fatal(err)
	}
	cleanups, err := store.OfflineAgentCleanups(context.Background(), "")
	if err != nil || len(cleanups) != 1 || cleanups[0].Command != "sudo vastora agent uninstall --purge" || strings.Contains(cleanups[0].Command, node.Credential) {
		t.Fatalf("unsafe offline cleanup report: %#v err=%v", cleanups, err)
	}
	var output strings.Builder
	if err := store.DecommissionApplications(context.Background(), true, true, "", func(message string) {
		output.WriteString(message)
		output.WriteByte('\n')
	}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := store.db.QueryRowContext(context.Background(), `SELECT state FROM agent_decommissions WHERE agent_id = ?`, node.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "abandoned" || !strings.Contains(output.String(), "Manual cleanup still required") {
		t.Fatalf("offline cleanup was not marked incomplete: state=%s output=%q", state, output.String())
	}
}

func waitForDecommissionTask(t *testing.T, store *Store, node AgentCredential) *AgentTask {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		task, err := store.ClaimNextTask(context.Background(), node.ID, node.Credential)
		if err != nil {
			t.Fatal(err)
		}
		if task != nil {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("decommission task was not queued")
	return nil
}
