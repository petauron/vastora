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
			node := enrollOrchestrationNode(t, store, "decommission-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.90", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.90", LANAddress: "10.0.0.90", EnabledKinds: []string{networking.KindLAN}})
			installCPA(t, store, node, "10.0.0.90")

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			finished := make(chan error, 1)
			messages := make(chan string, 10)
			go func() {
				finished <- store.DecommissionApplications(ctx, deleteData, func(message string) { messages <- message })
			}()

			task := waitForDecommissionTask(t, store, node)
			if task.Operation != "uninstall" || task.AppKey != cpaAppKey || task.DeleteData != deleteData {
				t.Fatalf("unexpected decommission task: %#v", task)
			}
			if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil); err != nil {
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
	node := enrollOrchestrationNode(t, store, "offline-decommission-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.91", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.91", LANAddress: "10.0.0.91", EnabledKinds: []string{networking.KindLAN}})
	installCPA(t, store, node, "10.0.0.91")
	if _, err := store.db.ExecContext(context.Background(), `UPDATE agents SET last_seen_at = ? WHERE id = ?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), node.ID); err != nil {
		t.Fatal(err)
	}
	err := store.DecommissionApplications(context.Background(), false, nil)
	if err == nil || !strings.Contains(err.Error(), "is offline") {
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
