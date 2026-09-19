package center

import (
	"context"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestManualUpgradeSupersedesOldFailureWithoutRewritingEvidence(t *testing.T) {
	for _, single := range []bool{false, true} {
		store := openOrchestrationStore(t)
		ctx := context.Background()
		node := enrollOrchestrationNode(t, store, "manually-updated", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.91", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.91", LANAddress: "10.0.0.91", EnabledKinds: []string{networking.KindLAN}})
		heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.134", true)
		now := store.now().UTC().Format(time.RFC3339Nano)
		if _, err := store.db.Exec(`INSERT INTO agent_updates(id,agent_id,target_version,state,last_error,created_at,updated_at) VALUES('old-failure',?,'0.1.0-alpha.129','failed','original failure',?,?)`, node.ID, now, now); err != nil {
			t.Fatal(err)
		}
		status, err := store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.135")
		if err != nil || status.Pending != 1 || status.Manual != 0 {
			t.Fatalf("stale failure blocks future rollout: %+v %v", status, err)
		}
		if single {
			if _, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.135"); err != nil {
				t.Fatal(err)
			}
		} else if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.135"); err != nil || len(queued) != 1 {
			t.Fatalf("rollout did not queue manually upgraded Agent: %v %v", queued, err)
		}
		var state, message string
		if err := store.db.QueryRow(`SELECT state,last_error FROM agent_updates WHERE id='old-failure'`).Scan(&state, &message); err != nil || state != "failed" || message != "original failure" {
			t.Fatalf("old evidence rewritten: %s %s %v", state, message, err)
		}
		store.Close()
	}
}

func TestNewerVersionDoesNotBypassRuntimeRecovery(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "recovery-blocked", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.92", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.92", LANAddress: "10.0.0.92", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.134", true)
	if _, err := store.db.Exec(`UPDATE agents SET runtime_recovery='operator verification required' WHERE id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.135"); err != nil || len(queued) != 0 {
		t.Fatalf("automatic update bypassed recovery: %v %v", queued, err)
	}
	if _, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.135"); err == nil {
		t.Fatal("individual update bypassed recovery")
	}
	if status, err := store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.135"); err != nil || status.Blocked != 1 || status.Manual != 0 || status.Pending != 0 {
		t.Fatalf("recovery incorrectly displayed as pending: %+v %v", status, err)
	}
}
