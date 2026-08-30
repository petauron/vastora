package center

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestDecommissionApplicationsUsesNormalAgentLifecycle(t *testing.T) {
	for _, deleteData := range []bool{false, true} {
		t.Run(map[bool]string{false: "keep-data", true: "delete-data"}[deleteData], func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
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
			if hostTask.Kind != "agent.decommission" || hostTask.DeleteData != deleteData {
				t.Fatalf("unexpected Agent host cleanup task: %#v", hostTask)
			}
			if err := store.CompleteTask(ctx, node.ID, node.Credential, hostTask.ID, hostTask.Attempt, true, "", nil, hostTask.RequiredRuntimeGeneration); err != nil {
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
