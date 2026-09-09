package center

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/networking"
)

func TestAgentUpdateRolloutWaitsForHostUpdate(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	updater := &fakeCenterUpdater{}
	server := NewServer(store, "", false)
	server.updates = updater
	server.agentBinariesDir = "unused-test-binaries"
	for _, test := range []struct {
		state string
		ready bool
	}{{"idle", true}, {"queued", false}, {"applying", false}, {"failed", false}, {"succeeded", true}, {"unknown", false}} {
		updater.status = deployapi.CenterUpdateExecution{Available: true, State: test.state}
		ready, err := server.agentUpdateRolloutReady(context.Background())
		if err != nil || ready != test.ready {
			t.Fatalf("state=%s ready=%v err=%v", test.state, ready, err)
		}
	}
	server.startupReady.Store(false)
	if ready, err := server.agentUpdateRolloutReady(context.Background()); err != nil || ready {
		t.Fatalf("startup did not block rollout: %v %v", ready, err)
	}
	server.startupReady.Store(true)
	server.agentBinariesDir = ""
	if ready, err := server.agentUpdateRolloutReady(context.Background()); err != nil || ready {
		t.Fatalf("missing binaries did not block rollout: %v %v", ready, err)
	}
}

func TestAgentUpdateRolloutWaitsForAppliedLocalIngress(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	store.now = func() time.Time { return now }
	local := enrollOrchestrationNode(t, store, "center-host", NodeCapabilities{Docker: true, Gateway: true},
		[]networking.Candidate{{Address: "10.0.0.10", Interface: "eth0", Kind: networking.KindLAN}},
		networking.Profile{ServiceAddress: "10.0.0.10", LANAddress: "10.0.0.10", EnabledKinds: []string{networking.KindLAN}})
	remote := enrollOrchestrationNode(t, store, "unrelated-remote", NodeCapabilities{Docker: true, Gateway: true},
		[]networking.Candidate{{Address: "10.0.0.11", Interface: "eth0", Kind: networking.KindLAN}},
		networking.Profile{ServiceAddress: "10.0.0.11", LANAddress: "10.0.0.11", EnabledKinds: []string{networking.KindLAN}})
	if _, err := store.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, setupGatewayBindingSetting, `{"publicAddress":"198.51.100.10","bindAddress":"10.0.0.10"}`); err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := store.db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{local.ID, remote.ID} {
		exec(`INSERT INTO gateway_components(gateway_node_id,desired_status,generation,applied_generation,status,updated_at) VALUES(?,'running',1,1,'ready',?)
			ON CONFLICT(gateway_node_id) DO UPDATE SET desired_status='running',status='ready',applied_generation=generation`, id, now.Format(time.RFC3339Nano))
		exec(`INSERT INTO gateway_states(gateway_node_id,desired_revision,applied_revision,desired_json,status,updated_at) VALUES(?,1,1,'{}','ready',?)
			ON CONFLICT(gateway_node_id) DO UPDATE SET status='ready',applied_revision=desired_revision`, id, now.Format(time.RFC3339Nano))
	}
	exec(`UPDATE node_listener_states SET status='ready',applied_revision=desired_revision WHERE node_id=?`, local.ID)
	exec(`UPDATE gateway_states SET status='failed' WHERE gateway_node_id=?`, remote.ID)
	server := NewServer(store, "", false)
	server.agentBinariesDir = "unused-test-binaries"
	assertReady := func(want bool) {
		t.Helper()
		ready, err := server.agentUpdateRolloutReady(ctx)
		if err != nil || ready != want {
			t.Fatalf("ready=%v want=%v err=%v", ready, want, err)
		}
	}
	assertReady(true) // An unrelated node's gateway failure must not block others.
	exec(`UPDATE gateway_states SET status='applying' WHERE gateway_node_id=?`, local.ID)
	assertReady(false)
	exec(`UPDATE gateway_states SET status='ready', applied_revision=desired_revision-1 WHERE gateway_node_id=?`, local.ID)
	assertReady(false)
	exec(`UPDATE gateway_states SET applied_revision=desired_revision WHERE gateway_node_id=?`, local.ID)
	exec(`UPDATE gateway_components SET status='applying' WHERE gateway_node_id=?`, local.ID)
	assertReady(false)
	exec(`UPDATE gateway_components SET status='ready', applied_generation=generation-1 WHERE gateway_node_id=?`, local.ID)
	assertReady(false)
	exec(`UPDATE gateway_components SET applied_generation=generation WHERE gateway_node_id=?`, local.ID)
	exec(`INSERT INTO node_listener_states(node_id,desired_revision,applied_revision,desired_json,status,updated_at) VALUES(?,1,0,'{}','applying',?) ON CONFLICT(node_id) DO UPDATE SET desired_revision=1,applied_revision=0,status='applying'`, local.ID, now.Format(time.RFC3339Nano))
	assertReady(false)
	exec(`UPDATE node_listener_states SET status='ready' WHERE node_id=?`, local.ID)
	assertReady(false)
	exec(`UPDATE node_listener_states SET applied_revision=desired_revision WHERE node_id=?`, local.ID)
	assertReady(true)
	exec(`UPDATE node_listener_states SET status='stopped' WHERE node_id=?`, local.ID)
	assertReady(true)
	exec(`UPDATE agents SET last_seen_at=? WHERE id=?`, now.Add(-time.Minute).Format(time.RFC3339Nano), local.ID)
	assertReady(false)
	exec(`UPDATE agents SET last_seen_at=? WHERE id=?`, now.Format(time.RFC3339Nano), local.ID)
	exec(`INSERT INTO agent_network_candidates(agent_id,address,interface_name,kind,observed_at) VALUES(?,'10.0.0.10','eth1','lan',?)`, remote.ID, now.Format(time.RFC3339Nano))
	assertReady(false) // Never pick an arbitrary owner when addresses are ambiguous.
}

type failedReadinessUpdater struct{ fakeCenterUpdater }

func (failedReadinessUpdater) CenterUpdateStatus(context.Context) (deployapi.CenterUpdateExecution, error) {
	return deployapi.CenterUpdateExecution{}, errors.New("updater unreachable")
}

func TestAgentUpdateRolloutReadinessFailsClosed(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	server := NewServer(store, "", false)
	server.agentBinariesDir = "unused-test-binaries"
	server.updates = &failedReadinessUpdater{}
	if ready, err := server.agentUpdateRolloutReady(context.Background()); err == nil || ready {
		t.Fatalf("updater error was ignored: %v %v", ready, err)
	}
	server.updates = nil
	if _, err := store.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, setupGatewayBindingSetting, "invalid"); err != nil {
		t.Fatal(err)
	}
	if ready, err := server.agentUpdateRolloutReady(context.Background()); err == nil || ready {
		t.Fatalf("invalid topology was ignored: %v %v", ready, err)
	}
}
