package agent_test

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/center"
)

func TestThreeXUINodeMigrationTaskMatchesAgentContract(t *testing.T) {
	payload, err := json.Marshal(center.AgentTask{
		Kind:    "application.command",
		ID:      "application-command-contract",
		Attempt: 1,
		NodeCommand: &center.ThreeXUINodeCommandTask{
			Action:              "reconcile",
			MigrationID:         "migration-contract",
			WorkerApplicationID: "worker-contract",
			Name:                "worker",
			Address:             "100.64.0.2",
			Port:                2053,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var task agent.DeploymentTask
	if err := decoder.Decode(&task); err != nil {
		t.Fatalf("Center node migration task does not match the Agent contract: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("Center node migration task has trailing data: %v", err)
	}
	if task.NodeCommand == nil || task.NodeCommand.MigrationID != "migration-contract" {
		t.Fatalf("Agent lost the node migration identity: %#v", task.NodeCommand)
	}
}
