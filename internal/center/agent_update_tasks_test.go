package center

import (
	"context"
	"errors"
	"github.com/petauron/vastora/internal/controlplane"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
)

func TestAgentUpdateRequiresHandoffAndTargetVersionReconnect(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "update-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.94", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.94", LANAddress: "10.0.0.94", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)

	queued, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.89")
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil {
		t.Fatal(err)
	}
	if task == nil || task.Kind != "agent.update" || task.ID != queued.ID || task.TargetVersion != "0.1.0-alpha.89" {
		t.Fatalf("unexpected Agent update task: %#v", task)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil, 0); err == nil {
		t.Fatal("Agent update completed before durable helper handoff")
	}
	begin := prepareUpdateHandoffForTest(t, store, node, *task)
	if err := begin(); err != nil {
		t.Fatal(err)
	}
	if err := begin(); !errors.Is(err, errExecutionAuthorization) {
		t.Fatalf("duplicate update handoff consumed permission twice: %v", err)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil, 0); err == nil {
		t.Fatal("Agent update completed before the target version reconnected")
	}
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.89", true)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil, 0); err != nil {
		t.Fatal(err)
	}

	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Version != "0.1.0-alpha.89" || agents[0].Update == nil || agents[0].Update.State != "succeeded" {
		t.Fatalf("updated Agent state was not exposed: %#v", agents)
	}
}

func TestAgentUpdateUncertainFailureDoesNotResumeWhenTargetReconnects(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "update-recovery-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.99", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.99", LANAddress: "10.0.0.99", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)

	queued, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.89")
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil || task == nil {
		t.Fatalf("claim update: %#v, %v", task, err)
	}
	begin := prepareUpdateHandoffForTest(t, store, node, *task)
	if err := begin(); err != nil {
		t.Fatal(err)
	}
	const recoveryRequired = "recovery required; schema-compatible candidate remains installed"
	if err := store.completeTaskWithDisposition(ctx, commitProjectionOnlyForTest, node.ID, node.Credential, task.ID, task.Attempt, false, recoveryRequired, nil, true); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.completeTaskWithDisposition(ctx, commitProjectionOnlyForTest, node.ID, node.Credential, task.ID, task.Attempt, false, recoveryRequired, nil, true); err != nil {
			t.Fatalf("recovery report replay failed: %v", err)
		}
	}
	var state, lastError string
	if err := store.db.QueryRowContext(ctx, `SELECT state, last_error FROM agent_updates WHERE id = ?`, task.ID).Scan(&state, &lastError); err != nil || state != "failed" || lastError != recoveryRequired {
		t.Fatalf("recovery no longer owns the update: state=%q error=%q err=%v", state, lastError, err)
	}
	var failedActivations int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_events WHERE task_id = ? AND event = 'failed'`, task.ID).Scan(&failedActivations); err != nil || failedActivations != 1 {
		t.Fatalf("replayed recovery reports duplicated activation events: count=%d err=%v", failedActivations, err)
	}
	if repeated, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.89"); err == nil {
		t.Fatalf("ordinary update bypassed manual disposition: %#v", repeated)
	}
	if _, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.90"); err == nil {
		t.Fatal("a competing version replaced an active recovery")
	}
	if nodes, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.90"); err != nil || len(nodes) != 0 {
		t.Fatalf("automatic rollout replaced an active recovery: %v %v", nodes, err)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil, 0); err == nil {
		t.Fatal("recovery succeeded without a target-version heartbeat")
	}
	if err := store.completeTaskWithDisposition(ctx, commitProjectionOnlyForTest, node.ID, node.Credential, task.ID, task.Attempt+1, false, "stale", nil, true); err == nil {
		t.Fatal("a stale attempt changed recovery state")
	}
	if err := begin(); !errors.Is(err, errExecutionAuthorization) {
		t.Fatalf("recovering update consumed handoff twice: %v", err)
	}
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.89", true)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil, 0); err == nil {
		t.Fatal("target heartbeat silently completed the failed update")
	}
	rollout, err := store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.89")
	if err != nil || rollout.Failed != 1 || rollout.Updated != 0 || rollout.Updating != 0 {
		t.Fatalf("new version hid unresolved failure: %+v %v", rollout, err)
	}

	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || agents[0].Update == nil || agents[0].Update.ID != queued.ID || agents[0].Update.State != "failed" || agents[0].Update.LastError != recoveryRequired {
		t.Fatalf("recovered update state = %#v", agents)
	}
	if err := store.completeTaskWithDisposition(ctx, commitProjectionOnlyForTest, node.ID, node.Credential, task.ID, task.Attempt, false, recoveryRequired, nil, true); err != nil {
		t.Fatalf("same failure evidence changed disposition: %v", err)
	}
}

func TestAgentUpdateRequiresAFeatureCapableOnlineAgent(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	node := enrollOrchestrationNode(t, store, "legacy-update-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.95", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.95", LANAddress: "10.0.0.95", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", false)

	if _, err := store.QueueAgentUpdate(context.Background(), node.ID, "0.1.0-alpha.89"); err == nil || !strings.Contains(err.Error(), "one manual update") {
		t.Fatalf("legacy Agent update was not rejected with migration guidance: %v", err)
	}
}

func TestAgentUpdateRolloutQueuesOnlineAgentsConcurrently(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	clock := store.now().UTC()
	store.now = func() time.Time { return clock }
	first := enrollOrchestrationNode(t, store, "rollout-a", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.96", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.96", LANAddress: "10.0.0.96", EnabledKinds: []string{networking.KindLAN}})
	second := enrollOrchestrationNode(t, store, "rollout-b", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.97", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.97", LANAddress: "10.0.0.97", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, first, "0.1.0-alpha.88", true)
	heartbeatAgentUpdateVersion(t, store, second, "0.1.0-alpha.88", true)

	queuedIDs, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89")
	if err != nil || len(queuedIDs) != 2 {
		t.Fatalf("rollout queue = %#v, %v", queuedIDs, err)
	}
	if queuedAgain, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89"); err != nil || len(queuedAgain) != 0 {
		t.Fatalf("duplicate rollout queue = %#v, %v", queuedAgain, err)
	}
	firstTask, err := store.ClaimNextTask(ctx, first.ID, first.Credential)
	if err != nil || firstTask == nil || firstTask.Kind != "agent.update" {
		t.Fatalf("claim first concurrent rollout task: %#v, %v", firstTask, err)
	}
	secondTask, err := store.ClaimNextTask(ctx, second.ID, second.Credential)
	if err != nil || secondTask == nil || secondTask.Kind != "agent.update" {
		t.Fatalf("claim second concurrent rollout task: %#v, %v", secondTask, err)
	}
	status, err := store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.89")
	if err != nil {
		t.Fatal(err)
	}
	if status.Total != 2 || status.Updated != 0 || status.Updating != 2 || status.Pending != 0 {
		t.Fatalf("unexpected rollout status: %#v", status)
	}
	clock = clock.Add(agentConnectedMaxAge)
	heartbeatAgentUpdateVersion(t, store, second, "0.1.0-alpha.88", true)
	status, err = store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.89")
	if err != nil || status.Total != 2 || status.Updating != 1 || status.Offline != 1 {
		t.Fatalf("offline Agent obscured the remaining online update: %#v, %v", status, err)
	}
}

func TestAgentUpdateRolloutSupersedesOnlyUnclaimedOlderTarget(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "unclaimed-update", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.98", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.98", LANAddress: "10.0.0.98", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)
	old, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.89")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.90")
	if err != nil || len(queued) != 1 || queued[0] != node.ID {
		t.Fatalf("new rollout queue = %v, %v", queued, err)
	}
	var state, reason string
	var attempt, failedEvents int
	if err := store.db.QueryRowContext(ctx, `SELECT state,attempt,last_error FROM agent_updates WHERE id=?`, old.ID).Scan(&state, &attempt, &reason); err != nil || state != "failed" || attempt != 0 || !strings.Contains(reason, "Superseded before claim") {
		t.Fatalf("retired update = %q attempt=%d reason=%q err=%v", state, attempt, reason, err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_events WHERE task_id=? AND event='failed'`, old.ID).Scan(&failedEvents); err != nil || failedEvents != 1 {
		t.Fatalf("retired update events = %d, %v", failedEvents, err)
	}
	task, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil || task == nil || task.Kind != "agent.update" || task.ID == old.ID || task.TargetVersion != "0.1.0-alpha.90" {
		t.Fatalf("claim replacement update = %#v, %v", task, err)
	}
}

func TestAgentUpdateRolloutKeepsClaimedOlderTarget(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "claimed-update", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.97", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.97", LANAddress: "10.0.0.97", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)
	old, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.89")
	if err != nil {
		t.Fatal(err)
	}
	if task, err := store.ClaimNextTask(ctx, node.ID, node.Credential); err != nil || task == nil || task.ID != old.ID {
		t.Fatalf("claim older update = %#v, %v", task, err)
	}
	if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.90"); err != nil || len(queued) != 0 {
		t.Fatalf("claimed update was replaced by rollout: %v, %v", queued, err)
	}
	if _, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.90"); err == nil {
		t.Fatal("claimed update was replaced by direct queue")
	}
	var state string
	var attempt int
	if err := store.db.QueryRowContext(ctx, `SELECT state,attempt FROM agent_updates WHERE id=?`, old.ID).Scan(&state, &attempt); err != nil || state != "running" || attempt != 1 {
		t.Fatalf("claimed update = %q attempt=%d err=%v", state, attempt, err)
	}
}

func TestAgentUpdateRolloutRetriesOnlyExplicitlyAbandonedFailure(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "abandoned-update", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.96", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.96", LANAddress: "10.0.0.96", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)
	old, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.89")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE agent_updates SET state='failed',attempt=1 WHERE id=?`, old.ID); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.90"); err != nil || len(queued) != 0 {
		t.Fatalf("unverified failure was retried: %v, %v", queued, err)
	}
	now := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,expires_at,created_at,updated_at,disposition)
		VALUES('abandoned-test-execution',?,?,'agent.update',1,'retired-test-session','test-digest',X'00','failed','reported',?,?,?,'abandon')`, node.ID, old.ID, now, now, now); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.90"); err != nil || len(queued) != 1 || queued[0] != node.ID {
		t.Fatalf("verified failure did not rejoin rollout: %v, %v", queued, err)
	}
}

func TestAgentUpdateRolloutEscapesMigrationPause(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "migration-rollout", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.98", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.98", LANAddress: "10.0.0.98", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?)`, executionClaimControlKey, `{"paused":true,"actor":"migration:75","updatedAt":"2026-09-21T00:00:00Z"}`); err != nil {
		t.Fatal(err)
	}
	queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89")
	if err != nil || len(queued) != 1 || queued[0] != node.ID {
		t.Fatalf("migration pause stranded rollout: %#v %v", queued, err)
	}
	if err := store.releaseMigrationExecutionPause(ctx); err != nil {
		t.Fatal(err)
	}
	control, err := store.ExecutionClaimControl(ctx)
	if err != nil || control.Paused || control.Actor != "system:agent-rollout" {
		t.Fatalf("completed rollout did not release migration pause: %+v %v", control, err)
	}
	var events int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM execution_claim_control_events WHERE paused=0 AND actor='system:agent-rollout'`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("migration release audit: %d %v", events, err)
	}
}

func TestAgentUpdateCanProceedPastTerminalBusinessExecution(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "terminal-business-execution", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.98", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.98", LANAddress: "10.0.0.98", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)
	session := "terminal-business-execution-session"
	if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	oldTask := AgentTask{ID: "old-business-task", Kind: "application.apply", Attempt: 1}
	authorization, err := store.PersistExecutionAuthorization(ctx, node.ID, session, oldTask)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StopExecution(ctx, node.ID, session, authorization.ID, true, "previous business operation requires review"); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89"); err != nil || len(queued) != 1 || queued[0] != node.ID {
		t.Fatalf("terminal business evidence blocked rollout: %#v %v", queued, err)
	}
	task, err := store.claimExecutionTask(ctx, node.ID, node.Credential, session, 0)
	if err != nil || task == nil || task.Kind != "agent.update" {
		t.Fatalf("independent Agent update was not claimed: %#v %v", task, err)
	}
	var state, disposition string
	if err := store.db.QueryRow(`SELECT state,disposition FROM task_executions WHERE id=?`, authorization.ID).Scan(&state, &disposition); err != nil || state != "unknown" || disposition != "" {
		t.Fatalf("business execution evidence changed: state=%q disposition=%q err=%v", state, disposition, err)
	}
}

func TestOutdatedAgentWaitsForUpdateBeforeClaimingMigratedWork(t *testing.T) {
	previousVersion := Version
	Version = "0.1.0-alpha.159"
	defer func() { Version = previousVersion }()

	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "rollout-fence", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.96", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.96", LANAddress: "10.0.0.96", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.158", true)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.queueNodeListenerState(ctx, tx, node.ID, store.now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if task, err := store.ClaimNextTask(ctx, node.ID, node.Credential); err != nil || task != nil {
		t.Fatalf("outdated Agent claimed migrated work before rollout: %#v, %v", task, err)
	}
	if queued, err := store.QueueAgentUpdates(ctx, Version); err != nil || len(queued) != 1 {
		t.Fatalf("queue rollout: %#v, %v", queued, err)
	}
	task, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil || task == nil || task.Kind != "agent.update" {
		t.Fatalf("Agent update did not precede migrated work: %#v, %v", task, err)
	}
}

func TestAgentUpdateRolloutLeavesFailedTargetsForManualRetry(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "rollout-failure", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.98", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.98", LANAddress: "10.0.0.98", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)

	if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89"); err != nil || len(queued) != 1 {
		t.Fatalf("queue rollout: %#v, %v", queued, err)
	}
	task, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil || task == nil {
		t.Fatalf("claim rollout: %#v, %v", task, err)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, false, "host update failed", nil, 0); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89"); err != nil || len(queued) != 0 {
		t.Fatalf("failed rollout was automatically retried: %#v, %v", queued, err)
	}
	if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.90"); err != nil || len(queued) != 0 {
		t.Fatalf("new release bypassed the failed update: %#v, %v", queued, err)
	}
	if status, err := store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.90"); err != nil || status.Blocked != 1 || status.Manual != 0 || status.Pending != 0 || status.Updating != 0 {
		t.Fatalf("blocked new release still appears pending: %#v, %v", status, err)
	}
	status, err := store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.89")
	if err != nil {
		t.Fatal(err)
	}
	if status.Failed != 1 || status.Pending != 0 || status.Updating != 0 {
		t.Fatalf("unexpected failed rollout status: %#v", status)
	}
}

func TestAgentUpdateRolloutSkipsOfflineAgentsUntilReconnect(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	clock := store.now().UTC()
	store.now = func() time.Time { return clock }
	node := enrollOrchestrationNode(t, store, "offline-update-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.95", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.95", LANAddress: "10.0.0.95", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)
	clock = clock.Add(agentConnectedMaxAge)

	if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89"); err != nil || len(queued) != 0 {
		t.Fatalf("offline Agent was queued automatically: %v, %v", queued, err)
	}
	if _, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.89"); err == nil || !strings.Contains(err.Error(), "must be online") {
		t.Fatalf("offline Agent accepted a manual update: %v", err)
	}
	status, err := store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.89")
	if err != nil || status.Total != 1 || status.Offline != 1 || status.Updating != 0 || status.Pending != 0 {
		t.Fatalf("offline rollout status = %#v, %v", status, err)
	}

	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)
	if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89"); err != nil || len(queued) != 1 || queued[0] != node.ID {
		t.Fatalf("reconnected Agent was not queued: %v, %v", queued, err)
	}
}

func TestAgentUpdateRolloutOfflineTasksRemainRecoverable(t *testing.T) {
	for _, state := range []string{"pending", "running", "installing"} {
		t.Run(state, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			clock := store.now().UTC()
			store.now = func() time.Time { return clock }
			node := enrollOrchestrationNode(t, store, "interrupted-update-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.96", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.96", LANAddress: "10.0.0.96", EnabledKinds: []string{networking.KindLAN}})
			heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)
			queued, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.89")
			if err != nil {
				t.Fatal(err)
			}
			var task *AgentTask
			var begin func() error
			if state != "pending" {
				task, err = store.ClaimNextTask(ctx, node.ID, node.Credential)
				if err != nil || task == nil || task.ID != queued.ID {
					t.Fatalf("claim update: %#v, %v", task, err)
				}
				begin = prepareUpdateHandoffForTest(t, store, node, *task)
			}
			if state == "installing" {
				if err := begin(); err != nil {
					t.Fatal(err)
				}
			}
			status, err := store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.89")
			if err != nil || status.Updating != 1 || status.Offline != 0 {
				t.Fatalf("online update status = %#v, %v", status, err)
			}

			clock = clock.Add(agentConnectedMaxAge)
			status, err = store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.89")
			if err != nil || status.Total != 1 || status.Offline != 1 || status.Updating != 0 || status.Updated != 0 || status.Failed != 0 || status.Pending != 0 {
				t.Fatalf("offline update kept rollout busy: %#v, %v", status, err)
			}
			if nodes, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89"); err != nil || len(nodes) != 0 {
				t.Fatalf("offline update was queued again: %v, %v", nodes, err)
			}
			var persistedState string
			if err := store.db.QueryRowContext(ctx, `SELECT state FROM agent_updates WHERE id = ?`, queued.ID).Scan(&persistedState); err != nil || persistedState != state {
				t.Fatalf("offline status changed durable task: state=%q err=%v", persistedState, err)
			}

			clock = clock.Add(taskLeaseDuration)
			heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.88", true)
			status, err = store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.89")
			if err != nil || status.Updating != 0 || status.Failed != 1 || status.Offline != 0 {
				t.Fatalf("reconnected update status = %#v, %v", status, err)
			}
			if nodes, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.89"); err != nil || len(nodes) != 0 {
				t.Fatalf("reconnect duplicated the durable update: %v, %v", nodes, err)
			}
			if state == "running" {
				if err := begin(); err == nil {
					t.Fatal("expired authorization accepted a late handoff before task recovery")
				}
				for poll := 0; poll < 2; poll++ {
					if replay, err := store.ClaimNextTask(ctx, node.ID, node.Credential); (err != nil && !errors.Is(err, errExecutionBlocked)) || replay != nil {
						t.Fatalf("expired update was automatically replayed: %#v, %v", replay, err)
					}
				}
				if err := begin(); err == nil {
					t.Fatal("failed attempt accepted a late handoff")
				}
				var attempt int64
				var message, lease string
				if err := store.db.QueryRowContext(ctx, `SELECT state, attempt, last_error, lease_expires_at FROM agent_updates WHERE id=?`, task.ID).Scan(&persistedState, &attempt, &message, &lease); err != nil {
					t.Fatal(err)
				}
				if persistedState != "failed" || attempt != task.Attempt || lease != "" || !strings.Contains(message, "manual verification") {
					t.Fatalf("interrupted attempt was not preserved: %s attempt=%d lease=%q error=%q", persistedState, attempt, lease, message)
				}
				if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.90"); err != nil || len(queued) != 0 {
					t.Fatalf("new rollout bypassed the interrupted attempt: %#v, %v", queued, err)
				}
				return
			}
			if state == "pending" {
				task, err = store.ClaimNextTask(ctx, node.ID, node.Credential)
				if err != nil || task == nil || task.ID != queued.ID {
					t.Fatalf("resume update: %#v, %v", task, err)
				}
				begin = prepareUpdateHandoffForTest(t, store, node, *task)
			}
			beginErr := begin()
			if state == "installing" {
				if !errors.Is(beginErr, errExecutionAuthorization) {
					t.Fatalf("reconnection reauthorized installing helper: %v", beginErr)
				}
			} else if beginErr != nil {
				t.Fatal(beginErr)
			}
			heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.89", true)
			if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", nil, 0); err != nil {
				t.Fatal(err)
			}
			status, err = store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.89")
			if err != nil || status.Updated != 1 || status.Updating != 0 || status.Offline != 0 {
				t.Fatalf("reconnected update did not complete: %#v, %v", status, err)
			}
		})
	}
}

func heartbeatAgentUpdateVersion(t *testing.T, store *Store, node AgentCredential, version string, supported bool) {
	t.Helper()
	if err := store.RecordAgentHeartbeat(context.Background(), node.ID, node.Credential, NodeHeartbeat{
		Version: version, Roles: []string{"worker"}, Capabilities: NodeCapabilities{Docker: true},
		ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration, RemoteUpdateSupported: supported,
	}); err != nil {
		t.Fatal(err)
	}
}

func prepareUpdateHandoffForTest(t *testing.T, store *Store, node AgentCredential, task AgentTask) func() error {
	t.Helper()
	ctx := context.Background()
	session := "update-task-test-current-process-session"
	if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	auth, err := store.PersistExecutionAuthorization(ctx, node.ID, session, task)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StartExecution(ctx, node.ID, session, auth.ID, auth.Digest); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckExecutionStep(ctx, node.ID, session, auth.ID, "handoff"); err != nil {
		t.Fatal(err)
	}
	return func() error {
		return store.beginAgentUpdateExecution(ctx, node.ID, node.Credential, task.ID, task.Attempt, auth.ID, session)
	}
}

func TestAgentUpdateRolloutVersionDoesNotCompleteActiveTask(t *testing.T) {
	for _, state := range []string{"pending", "running", "installing"} {
		for _, version := range []string{"0.1.0-alpha.124", "0.1.0-alpha.125"} {
			t.Run(state+"/"+version, func(t *testing.T) {
				store := openOrchestrationStore(t)
				defer store.Close()
				ctx := context.Background()
				clock := store.now().UTC()
				store.now = func() time.Time { return clock }
				node := enrollOrchestrationNode(t, store, "version-observation", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.94", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.94", LANAddress: "10.0.0.94", EnabledKinds: []string{networking.KindLAN}})
				heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.123", true)
				queued, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.124")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.Exec(`UPDATE agent_updates SET state=? WHERE id=?`, state, queued.ID); err != nil {
					t.Fatal(err)
				}
				heartbeatAgentUpdateVersion(t, store, node, version, true)
				status, err := store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.124")
				if err != nil || status.Total != 1 || status.Updating != 1 || status.Updated != 0 {
					t.Fatalf("active operation hidden by version observation: %+v %v", status, err)
				}
				clock = clock.Add(agentConnectedMaxAge + time.Second)
				status, err = store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.124")
				if err != nil || status.Total != 1 || status.Offline != 1 || status.Updating != 0 || status.Updated != 0 {
					t.Fatalf("offline operation reported complete or kept rollout busy: %+v %v", status, err)
				}
				var actual string
				if err := store.db.QueryRow(`SELECT state FROM agent_updates WHERE id=?`, queued.ID).Scan(&actual); err != nil || actual != state {
					t.Fatalf("status observation changed execution state: %q %v", actual, err)
				}
			})
		}
	}
}

func TestAgentUpdateRolloutBlockedTasksDoNotStayBusy(t *testing.T) {
	for _, test := range []struct {
		name, state, message string
		stalled              bool
	}{
		{"reported recovery failure", "installing", "protected recovery does not match this update", false},
		{"unclaimed task", "pending", "", true},
		{"stalled download", "running", "", true},
		{"silent helper", "installing", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			clock := store.now().UTC()
			store.now = func() time.Time { return clock }
			node := enrollOrchestrationNode(t, store, "blocked-update", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.94", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.94", LANAddress: "10.0.0.94", EnabledKinds: []string{networking.KindLAN}})
			heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.123", true)
			queued, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.124")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE agent_updates SET state=?,last_error=? WHERE id=?`, test.state, test.message, queued.ID); err != nil {
				t.Fatal(err)
			}
			if test.stalled {
				clock = clock.Add(agentUpdateProgressTimeout)
			}
			heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.123", true)
			status, err := store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.124")
			if err != nil || status.Updating != 0 || status.Failed != 1 || status.Offline != 0 {
				t.Fatalf("blocked task kept rollout busy: %#v %v", status, err)
			}
			var state string
			if err := store.db.QueryRow(`SELECT state FROM agent_updates WHERE id=?`, queued.ID).Scan(&state); err != nil || state != test.state {
				t.Fatalf("presentation cancelled durable recovery: %s %v", state, err)
			}
			if queued, err := store.QueueAgentUpdates(ctx, "0.1.0-alpha.124"); err != nil || len(queued) != 0 {
				t.Fatalf("blocked task was duplicated: %v %v", queued, err)
			}
			heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.124", true)
			status, err = store.AgentUpdateRolloutStatus(ctx, "0.1.0-alpha.124")
			if err != nil || status.Updated != 0 || status.Failed != 1 {
				t.Fatalf("version heartbeat erased unresolved execution: %#v %v", status, err)
			}
		})
	}
}
