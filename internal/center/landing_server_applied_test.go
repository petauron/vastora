package center

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/petauron/vastora/internal/landing"
)

func TestLandingServerAppliedStateChangesOnlyAfterSuccessfulReceipt(t *testing.T) {
	store := openMeridianSharedEndpointSnapshotFixture(t)
	ctx := context.Background()
	var nodeID string
	if err := store.db.QueryRow(`SELECT node_id FROM applications WHERE id='snapshot-shared-app'`).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	queue := func(plan *landing.ServerPlan) *AgentTask {
		t.Helper()
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := store.queueLandingServer(ctx, tx, nodeID, plan); err != nil {
			t.Fatal(err)
		}
		task, err := store.claimLandingServerTask(ctx, tx, nodeID)
		if err != nil || task == nil {
			t.Fatalf("native landing task unavailable: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return task
	}
	check := func(expected *landing.ServerState, revision int64) {
		t.Helper()
		var raw string
		var applied int64
		if err := store.db.QueryRow(`SELECT applied_json,applied_revision FROM landing_server_states WHERE node_id=?`, nodeID).Scan(&raw, &applied); err != nil {
			t.Fatal(err)
		}
		want := "{}"
		if expected != nil {
			data, err := json.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			want = string(data)
		}
		if raw != want || applied != revision {
			t.Fatalf("applied state changed without matching successful receipt: revision=%d expected=%d matches=%t", applied, revision, raw == want)
		}
	}
	peer := &landing.PeerIdentity{ID: "tailnet-landing-applied", PublicKey: "nodekey:test-applied", Address: "100.64.0.61"}
	plan := &landing.ServerPlan{Address: peer.Address, Sources: []landing.AuthorizedNode{{Address: "100.64.0.62", TCPOnly: true}}}
	first := queue(plan)
	check(nil, 0)
	if _, err := store.db.Exec(`UPDATE landing_server_states SET applied_json='not-json' WHERE node_id=?`, nodeID); err == nil {
		t.Fatal("fresh schema accepted invalid applied JSON")
	}
	if err := store.completeLandingServer(ctx, commitProjectionOnlyForTest, nodeID, first.Revision, first.Attempt, true, peer); err != nil {
		t.Fatal(err)
	}
	check(first.LandingServerState, first.Revision)
	plan.Sources = append(plan.Sources, landing.AuthorizedNode{Address: "100.64.0.63", TCPOnly: true})
	failed := queue(plan)
	check(first.LandingServerState, first.Revision)
	if err := store.completeLandingServer(ctx, commitProjectionOnlyForTest, nodeID, failed.Revision, failed.Attempt, false, nil); err != nil {
		t.Fatal(err)
	}
	check(first.LandingServerState, first.Revision)
	third := queue(plan)
	if err := store.completeLandingServer(ctx, commitProjectionOnlyForTest, nodeID, first.Revision, first.Attempt, true, peer); err != nil {
		t.Fatal(err)
	}
	check(first.LandingServerState, first.Revision)
	if err := store.completeLandingServer(ctx, commitProjectionOnlyForTest, nodeID, third.Revision, third.Attempt, true, nil); err == nil {
		t.Fatal("missing peer evidence was accepted")
	}
	check(first.LandingServerState, first.Revision)
	if err := store.completeLandingServer(ctx, commitProjectionOnlyForTest, nodeID, third.Revision, third.Attempt, true, peer); err != nil {
		t.Fatal(err)
	}
	check(third.LandingServerState, third.Revision)
	if err := store.completeLandingServer(ctx, commitProjectionOnlyForTest, nodeID, third.Revision, third.Attempt, true, peer); err != nil {
		t.Fatal(err)
	}
	check(third.LandingServerState, third.Revision)
	// A source-only update must record its new authority even when the landing
	// Agent reports the exact same authenticated private peer.
	plan.Sources = plan.Sources[:1]
	fourth := queue(plan)
	check(third.LandingServerState, third.Revision)
	if err := store.completeLandingServer(ctx, commitProjectionOnlyForTest, nodeID, fourth.Revision, fourth.Attempt, true, peer); err != nil {
		t.Fatal(err)
	}
	check(fourth.LandingServerState, fourth.Revision)
	stop := queue(nil)
	check(fourth.LandingServerState, fourth.Revision)
	if err := store.completeLandingServer(ctx, commitProjectionOnlyForTest, nodeID, stop.Revision, stop.Attempt, true, nil); err != nil {
		t.Fatal(err)
	}
	check(stop.LandingServerState, stop.Revision)
}

func TestLandingServerQueueAndClaimFreezeOnlyDuringCutover(t *testing.T) {
	for _, phase := range []string{"backup", "import", "publish", "project", "verify", "retire", "complete", "not_required"} {
		t.Run(phase, func(t *testing.T) {
			store := openMeridianSharedEndpointSnapshotFixture(t)
			ctx := context.Background()
			var nodeID string
			if err := store.db.QueryRow(`SELECT node_id FROM applications WHERE id='snapshot-shared-app'`).Scan(&nodeID); err != nil {
				t.Fatal(err)
			}
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			plan := &landing.ServerPlan{Address: "100.64.0.61"}
			if err := store.queueLandingServer(ctx, tx, nodeID, plan); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`UPDATE meridian_cutover SET state=? WHERE id=1`, phase); err != nil {
				t.Fatal(err)
			}
			if err := store.queueLandingServer(ctx, tx, nodeID, plan); err != nil {
				t.Fatal(err)
			}
			task, err := store.claimLandingServerTask(ctx, tx, nodeID)
			if err != nil {
				t.Fatal(err)
			}
			allowed := phase == "complete" || phase == "not_required"
			var revision, attempt int64
			if err := tx.QueryRow(`SELECT desired_revision,attempt FROM landing_server_states WHERE node_id=?`, nodeID).Scan(&revision, &attempt); err != nil {
				t.Fatal(err)
			}
			if allowed {
				if task == nil || revision != 2 || attempt != 1 {
					t.Fatal("completed cutover blocked native landing service work")
				}
			} else if task != nil || revision != 1 || attempt != 0 {
				t.Fatal("ownership transition released or replaced an existing landing intent")
			}
		})
	}
}

func TestLandingServerAuthorizationComparisonIgnoresRevisionAndOrder(t *testing.T) {
	previous := landing.ServerState{NodeID: "landing", Revision: 1, Plan: &landing.ServerPlan{Revision: 1, Address: "100.64.0.8",
		Sources: []landing.AuthorizedNode{{Address: "100.64.0.9", TCPOnly: true}, {Address: "100.64.0.10"}}}}
	raw, err := json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	current := landing.ServerState{NodeID: "landing", Revision: 2, Plan: &landing.ServerPlan{Revision: 2, Address: "100.64.0.8",
		Sources: []landing.AuthorizedNode{{Address: "100.64.0.10"}, {Address: "100.64.0.9", TCPOnly: true}}}}
	if !sameLandingServerAuthorization(raw, current) {
		t.Fatal("unchanged authorization was treated as a new source grant")
	}
	current.Plan.Sources[0].TCPOnly = true
	if sameLandingServerAuthorization(raw, current) {
		t.Fatal("changed protocol permission was ignored")
	}
	current.Plan = nil
	if sameLandingServerAuthorization(raw, current) || sameLandingServerAuthorization([]byte(`{}`), current) {
		t.Fatal("stop or missing prior receipt was treated as unchanged authorization")
	}
}

func TestLandingServerClaimRecomposesIntentFrozenBeforeCompletedCutover(t *testing.T) {
	store, egressID, _, _ := openMeridianRuntimeIdentityFixture(t)
	ctx := context.Background()
	var appliedBefore []byte
	if err := store.db.QueryRow(`SELECT applied_json FROM landing_server_states WHERE node_id=?`, egressID).Scan(&appliedBefore); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	plan := &landing.ServerPlan{Address: "100.64.0.62", Sources: []landing.AuthorizedNode{
		{Address: "100.64.0.61", TCPOnly: true}, {Address: "100.64.0.64", TCPOnly: true},
	}}
	if err := store.queueLandingServer(ctx, tx, egressID, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE meridian_cutover SET state='backup' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if task, err := store.claimLandingServerTask(ctx, tx, egressID); err != nil || task != nil {
		t.Fatalf("old intent crossed the transition fence: err=%v", err)
	}
	if _, err := tx.Exec(`UPDATE meridian_cutover SET state='complete' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	// Do not explicitly queue a replacement here: claiming the frozen old
	// intent itself must discard the source absent from current authority.
	task, err := store.claimLandingServerTask(ctx, tx, egressID)
	if err != nil || task == nil {
		t.Fatalf("canonical landing intent was not claimable: %v", err)
	}
	if task.Revision != 3 || task.LandingServerState.Plan == nil || len(task.LandingServerState.Plan.Sources) != 1 ||
		task.LandingServerState.Plan.Sources[0] != (landing.AuthorizedNode{Address: "100.64.0.61", TCPOnly: true}) {
		t.Fatal("claim released obsolete authorization instead of a newly composed revision")
	}
	var appliedAfter []byte
	if err := tx.QueryRow(`SELECT applied_json FROM landing_server_states WHERE node_id=?`, egressID).Scan(&appliedAfter); err != nil || string(appliedAfter) != string(appliedBefore) {
		t.Fatalf("recomposing claim fabricated an applied receipt: %v", err)
	}
}

func TestLandingServerClaimRejectsUnrecoverableFrozenIntent(t *testing.T) {
	for _, mutation := range []string{"missing-source-pin", "stop-referenced-service", "replace-referenced-address"} {
		t.Run(mutation, func(t *testing.T) {
			store, egressID, _, _ := openMeridianRuntimeIdentityFixture(t)
			ctx := context.Background()
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			plan := &landing.ServerPlan{Address: "100.64.0.62", Sources: []landing.AuthorizedNode{{Address: "100.64.0.61", TCPOnly: true}}}
			switch mutation {
			case "missing-source-pin":
				if _, err := tx.Exec(`UPDATE meridian_endpoints SET source_peer_json='{}' WHERE id=?`, sharedSnapshotEndpointID); err != nil {
					t.Fatal(err)
				}
			case "stop-referenced-service":
				plan = nil
			case "replace-referenced-address":
				plan.Address = "100.64.0.65"
			}
			if err := store.queueLandingServer(ctx, tx, egressID, plan); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`UPDATE meridian_cutover SET state='complete' WHERE id=1`); err != nil {
				t.Fatal(err)
			}
			if task, err := store.claimLandingServerTask(ctx, tx, egressID); err == nil || task != nil {
				t.Fatal("an inconsistent old intent became a task")
			}
			var state string
			var attempt int64
			if err := tx.QueryRow(`SELECT status,attempt FROM landing_server_states WHERE node_id=?`, egressID).Scan(&state, &attempt); err != nil || state != "pending" || attempt != 0 {
				t.Fatalf("rejected intent was claimed: status=%s attempt=%d err=%v", state, attempt, err)
			}
		})
	}
}
