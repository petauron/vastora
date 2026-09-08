package agent

import (
	"context"
	"testing"

	"github.com/petauron/vastora/internal/landing"
)

type recordingLandingServer struct{ applied, removed int }

func (p *recordingLandingServer) Apply(context.Context, string, landing.ServerPlan) error {
	p.applied++
	return nil
}
func (p *recordingLandingServer) Remove(context.Context, string, uint64) error {
	p.removed++
	return nil
}

func TestLandingServerTaskIdentityAndNativeExecution(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SaveConnection(ctx, testConnection(t, "node-1", "node", "https://center.example.com", "credential")); err != nil {
		t.Fatal(err)
	}
	provisioner := &recordingLandingServer{}
	client := Client{LandingServer: provisioner} // No Docker capability required.
	task := DeploymentTask{Kind: "landing.server.apply", Revision: 1, LandingServerState: &landing.ServerState{
		NodeID: "node-1", Revision: 1, Plan: &landing.ServerPlan{Revision: 1, Address: "100.64.0.8"},
	}}
	if err := client.applyLandingServerTask(ctx, store, task); err != nil {
		t.Fatal(err)
	}
	if provisioner.applied != 1 {
		t.Fatal("native installation was not dispatched")
	}
	task.LandingServerState.NodeID = "other-node"
	if err := client.applyLandingServerTask(ctx, store, task); err == nil {
		t.Fatal("foreign node accepted")
	}
	task.LandingServerState.NodeID = "node-1"
	task.Revision = 2
	if err := client.applyLandingServerTask(ctx, store, task); err == nil {
		t.Fatal("mismatched task revision accepted")
	}
	task.LandingServerState.Revision, task.LandingServerState.Plan = 2, nil
	if err := client.applyLandingServerTask(ctx, store, task); err != nil {
		t.Fatal(err)
	}
	if provisioner.applied != 1 || provisioner.removed != 1 {
		t.Fatal("unexpected host operations")
	}
	if !taskReconcilesCompleteDesiredState(task.Kind) {
		t.Fatal("interrupted landing tasks cannot resume")
	}
}
