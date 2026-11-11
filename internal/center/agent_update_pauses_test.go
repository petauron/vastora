package center

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/petauron/vastora/internal/networking"
)

func TestAgentUpdatePausesConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		valid      bool
	}{
		{"selected", `{"pausedAgentIds":["pilot"]}`, true},
		{"none", `{"pausedAgentIds":[]}`, true},
		{"null", `{"pausedAgentIds":null}`, false},
		{"missing", `{}`, false},
		{"unknown", `{"pausedAgentIds":[],"all":true}`, false},
		{"duplicate", `{"pausedAgentIds":["pilot","pilot"]}`, false},
		{"empty", `{"pausedAgentIds":[""]}`, false},
		{"trailing", `{"pausedAgentIds":[]} {}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "agent-update-pauses.json"), []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			pauses, err := readAgentUpdatePauses(dir)
			if (err == nil) != tc.valid {
				t.Fatalf("pauses=%v error=%v", pauses, err)
			}
			if tc.valid && pauses == nil {
				t.Fatal("explicit pause list must be loaded")
			}
		})
	}
	pauses, err := readAgentUpdatePauses(t.TempDir())
	if err != nil || pauses != nil {
		t.Fatalf("missing file changed existing policy: %v %v", pauses, err)
	}
}

func TestAgentUpdatePausesFencesQueueAndExistingPendingTask(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	pilot := enrollOrchestrationNode(t, store, "pilot", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.94", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.94", LANAddress: "10.0.0.94", EnabledKinds: []string{networking.KindLAN}})
	other := enrollOrchestrationNode(t, store, "other", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.95", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.95", LANAddress: "10.0.0.95", EnabledKinds: []string{networking.KindLAN}})
	for _, node := range []AgentCredential{pilot, other} {
		heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)
	}
	if _, err := store.QueueAgentUpdate(ctx, other.ID, "0.1.0-alpha.89"); err != nil {
		t.Fatal(err)
	}
	store.agentUpdatePauses = map[string]bool{other.ID: true}
	if _, err := store.QueueAgentUpdate(ctx, other.ID, "0.1.0-alpha.89"); err == nil {
		t.Fatal("manual upgrade escaped pause")
	}
	ids, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89")
	if err != nil || len(ids) != 1 || ids[0] != pilot.ID {
		t.Fatalf("rollout: %v %v", ids, err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.claimAgentUpdate(ctx, tx, other.ID)
	_ = tx.Rollback()
	if err != nil || task != nil {
		t.Fatalf("existing excluded task claimed: %v %v", task, err)
	}
	status, err := store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.89")
	if err != nil || !status.Paused || status.Total != 1 || status.Excluded != 1 {
		t.Fatalf("status: %+v %v", status, err)
	}
}
