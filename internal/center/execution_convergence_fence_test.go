package center

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
)

func TestControllerConvergenceStopsBeforeReadingPlansWithUnresolvedExecution(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "convergence-fence", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.19", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.19", LANAddress: "10.0.0.19", EnabledKinds: []string{networking.KindLAN}})
	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleMaster, Config: json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`)})
	if err != nil {
		t.Fatal(err)
	}
	session := "convergence-test-current-process-session"
	if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	grant, err := store.PersistExecutionAuthorization(ctx, node.ID, session, AgentTask{ID: deployment.ID, Kind: "application.apply", Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	// Poison the first plan read. The execution fence must return before any
	// plan processing, and removing the fence must make this sentinel observable.
	if _, err := store.db.Exec(`CREATE TEMP VIEW three_x_ui_migrations AS SELECT * FROM missing_convergence_test_table`); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"offered", "running", "helper_running", "failed", "unknown"} {
		if _, err := store.db.Exec(`UPDATE task_executions SET state=? WHERE id=?`, state, grant.ID); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			if err := store.resumeThreeXUIControllerConvergence(ctx); err != nil {
				t.Fatalf("%s bypassed execution fence: %v", state, err)
			}
		}
	}
	if _, err := store.db.Exec(`UPDATE task_executions SET disposition='abandon' WHERE id=?`, grant.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.resumeThreeXUIControllerConvergence(ctx); err == nil {
		t.Fatal("sentinel did not prove the plan read was fenced")
	}
}
