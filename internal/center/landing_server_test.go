package center

import (
	"context"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

func TestLandingSelectionUsesManagedNodeAndQueuesDisable(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`UPDATE agents SET tailscale_ownership='managed',last_seen_at=? WHERE id='agent-v3'`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO agent_network_profiles(agent_id,service_address,headscale_address,enabled_kinds_json,confirmed_at,candidate_observed_at)
 VALUES('agent-v3','100.64.0.8','100.64.0.8','["headscale"]',?,?) ON CONFLICT(agent_id) DO UPDATE SET headscale_address=excluded.headscale_address`, now, now); err != nil {
		t.Fatal(err)
	}
	if err := store.SelectLanding(ctx, LandingSelection{NodeID: "100.64.0.9"}); err == nil {
		t.Fatal("arbitrary address accepted as node")
	}
	if err := store.SelectLanding(ctx, LandingSelection{NodeID: "agent-v3"}); err != nil {
		t.Fatal(err)
	}
	view, err := store.Landing(ctx)
	if err != nil || view.NodeID != "agent-v3" || view.Revision != 1 || view.Status != "pending" {
		t.Fatalf("selection not queued: %+v %v", view, err)
	}
	if err := store.SelectLanding(ctx, LandingSelection{}); err == nil {
		t.Fatal("stale selection overwrote current node")
	}
	if err := store.SelectLanding(ctx, LandingSelection{Revision: 1}); err != nil {
		t.Fatal(err)
	}
	view, err = store.Landing(ctx)
	if err != nil || view.NodeID != "" || view.Revision != 2 {
		t.Fatal("selection not disabled")
	}
	var state string
	var stopPlan int
	if err := store.db.QueryRow(`SELECT json_extract(desired_json,'$.plan') IS NULL FROM landing_server_states WHERE node_id='agent-v3'`).Scan(&stopPlan); err != nil || stopPlan != 1 {
		t.Fatal("disable retained a server plan")
	}
	if err := store.db.QueryRow(`SELECT status FROM landing_server_states WHERE node_id='agent-v3'`).Scan(&state); err != nil || state != "pending" {
		t.Fatal("native stop was not queued")
	}
}

func TestLandingServerMigrationAndTaskRevisions(t *testing.T) {
	directory := t.TempDir()
	legacy := legacyMigrationStore(t, directory, 64)
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	queue := func(plan *landing.ServerPlan) {
		t.Helper()
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := store.queueLandingServer(ctx, tx, "agent-v3", plan); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	claim := func() *AgentTask {
		t.Helper()
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		task, err := store.claimLandingServerTask(ctx, tx, "agent-v3")
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return task
	}
	queue(&landing.ServerPlan{Address: "100.64.0.8"})
	first := claim()
	if first == nil || first.Revision != 1 || first.LandingServerState.Plan == nil {
		t.Fatal("missing native landing task")
	}
	queue(nil)
	second := claim()
	if second == nil || second.Revision != 2 || second.LandingServerState.Plan != nil {
		t.Fatal("missing stop revision")
	}
	if err := store.completeLandingServer(ctx, "agent-v3", first.Revision, first.Attempt, true, nil); err != nil {
		t.Fatal(err)
	}
	var applied int64
	if err := store.db.QueryRow(`SELECT applied_revision FROM landing_server_states WHERE node_id='agent-v3'`).Scan(&applied); err != nil || applied != 0 {
		t.Fatal("old completion overwrote current revision")
	}
	if err := store.completeLandingServer(ctx, "agent-v3", second.Revision, second.Attempt, true, nil); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := store.db.QueryRow(`SELECT status FROM landing_server_states WHERE node_id='agent-v3'`).Scan(&status); err != nil || status != "stopped" {
		t.Fatal("stop not completed")
	}
	if claim() != nil {
		t.Fatal("completed stop was claimed again")
	}
}
