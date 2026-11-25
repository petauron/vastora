package center

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/nodediagnostics"
	"github.com/petauron/vastora/internal/osreinstall"
	"github.com/petauron/vastora/internal/platform"
)

func TestAgentVersionCompatibilityMatrix(t *testing.T) {
	for kind, minVersion := range minAgentVersionByTaskKind {
		if kind == "agent.update" {
			if minVersion != "" {
				t.Fatalf("minAgentVersionByTaskKind[%q] = %q, want empty", kind, minVersion)
			}
			continue
		}
		if _, ok := normalizeSemver(minVersion); !ok {
			t.Fatalf("minAgentVersionByTaskKind[%q] = %q is not valid semver", kind, minVersion)
		}
	}

	previousVersion := Version
	defer func() { Version = previousVersion }()

	cases := []struct {
		name          string
		centerVersion string
		agentVersion  string
		kind          string
		want          bool
	}{
		{name: "agent update always allowed for legacy agent", centerVersion: "0.3.1", agentVersion: "0.1.0-alpha.88", kind: "agent.update", want: true},
		{name: "agent update allowed for empty agent version", centerVersion: "0.3.1", agentVersion: "", kind: "agent.update", want: true},
		{name: "compatible older release claims application apply", centerVersion: "0.3.1", agentVersion: "0.2.3", kind: "application.apply", want: true},
		{name: "exact baseline version claims application apply", centerVersion: "0.3.1", agentVersion: "0.2.0", kind: "application.apply", want: true},
		{name: "v-prefixed compatible agent claims task", centerVersion: "0.3.1", agentVersion: "v0.2.3", kind: "application.apply", want: true},
		{name: "matching center version claims application apply", centerVersion: "0.3.1", agentVersion: "0.3.1", kind: "application.apply", want: true},
		{name: "pre-baseline stable agent fenced from application apply", centerVersion: "0.3.1", agentVersion: "0.1.0", kind: "application.apply", want: false},
		{name: "pre-baseline alpha agent fenced from application apply", centerVersion: "0.3.1", agentVersion: "0.1.0-alpha.473", kind: "application.apply", want: false},
		{name: "empty agent version fails closed on ordinary task", centerVersion: "0.3.1", agentVersion: "", kind: "application.apply", want: false},
		{name: "whitespace agent version fails closed on ordinary task", centerVersion: "0.3.1", agentVersion: "   ", kind: "application.apply", want: false},
		{name: "older alpha agent fenced from listener apply", centerVersion: "0.1.0-alpha.159", agentVersion: "0.1.0-alpha.158", kind: "node.listener.apply", want: false},
		{name: "matching dev prerelease build stays compatible", centerVersion: "0.1.0-dev", agentVersion: "0.1.0-dev", kind: "application.apply", want: true},
		{name: "matching lab prerelease build stays compatible", centerVersion: "0.1.0-940lab.2", agentVersion: "0.1.0-940lab.2", kind: "application.apply", want: true},
		{name: "synthetic non-semver test version remains unconstrained", centerVersion: "0.3.1", agentVersion: "test", kind: "application.apply", want: true},
		{name: "os reinstall requires exact center version", centerVersion: "0.3.1", agentVersion: "0.2.3", kind: osreinstall.TaskKind, want: false},
		{name: "os reinstall allows exact center version", centerVersion: "0.3.1", agentVersion: "0.3.1", kind: osreinstall.TaskKind, want: true},
		{name: "os reinstall rejects empty center version", centerVersion: "", agentVersion: "", kind: osreinstall.TaskKind, want: false},
		{name: "undeclared task kind fails closed to center version", centerVersion: "0.3.1", agentVersion: "0.2.3", kind: "future.unregistered.task", want: false},
		{name: "undeclared task kind allows matching center version", centerVersion: "0.3.1", agentVersion: "0.3.1", kind: "future.unregistered.task", want: true},
		{name: "undeclared task kind fails closed on empty center version", centerVersion: "", agentVersion: "0.3.1", kind: "future.unregistered.task", want: false},
		{name: "undeclared task kind fails closed on empty center version even for synthetic agent", centerVersion: "", agentVersion: "test", kind: "future.unregistered.task", want: false},
		{name: "undeclared task kind fails closed on empty agent version", centerVersion: "0.3.1", agentVersion: "", kind: "future.unregistered.task", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Version = tc.centerVersion
			if got := agentVersionCompatibleForTaskKind(tc.agentVersion, tc.kind); got != tc.want {
				t.Fatalf("agentVersionCompatibleForTaskKind(%q, %q) with Center %q = %v, want %v", tc.agentVersion, tc.kind, tc.centerVersion, got, tc.want)
			}
			if got := agentVersionCompatibleForTask(tc.agentVersion, &AgentTask{Kind: tc.kind}); got != tc.want {
				t.Fatalf("agentVersionCompatibleForTask(%q, %q) with Center %q = %v, want %v", tc.agentVersion, tc.kind, tc.centerVersion, got, tc.want)
			}
		})
	}
	if !agentVersionCompatibleForTask("0.1.0", nil) {
		t.Fatal("nil task was unexpectedly rejected")
	}
}

func TestCompatibleOlderAgentClaimsApplicationDeploymentAndPrioritizesExplicitUpdate(t *testing.T) {
	previousVersion := Version
	Version = "0.3.1"
	defer func() { Version = previousVersion }()

	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()

	worker := enrollOrchestrationNode(t, store, "compatible-worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.91", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.91", LANAddress: "10.0.0.91", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, worker, "0.2.3", true)

	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{
		AgentID:                worker.ID,
		AppKey:                 cpaAppKey,
		Config:                 json.RawMessage(`{"debug":false}`),
		AuthorizedCapabilities: testCapabilityGrant("root"),
	})
	if err != nil {
		t.Fatal(err)
	}

	task, err := store.ClaimNextTask(ctx, worker.ID, worker.Credential)
	if err != nil || task == nil || task.Kind != "application.apply" || task.ID != deployment.ID {
		t.Fatalf("compatible 0.2.3 Agent did not claim application deployment under Center %s: %#v, %v", Version, task, err)
	}

	updating := enrollOrchestrationNode(t, store, "updating-worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.92", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.92", LANAddress: "10.0.0.92", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, updating, "0.2.3", true)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{
		AgentID:                updating.ID,
		AppKey:                 cpaAppKey,
		Config:                 json.RawMessage(`{"debug":false}`),
		AuthorizedCapabilities: testCapabilityGrant("root"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueAgentUpdate(ctx, updating.ID, Version); err != nil {
		t.Fatal(err)
	}

	task, err = store.ClaimNextTask(ctx, updating.ID, updating.Credential)
	if err != nil || task == nil || task.Kind != "agent.update" || task.TargetVersion != Version {
		t.Fatalf("explicit Agent update did not precede pending deployment: %#v, %v", task, err)
	}
}

func TestPerTaskCompatibilitySkipsIncompatibleKindWithoutBlockingCompatibleWork(t *testing.T) {
	previousVersion := Version
	previousListenerMin := minAgentVersionByTaskKind["node.listener.apply"]
	Version = "0.4.0"
	minAgentVersionByTaskKind["node.listener.apply"] = "0.4.0"
	defer func() {
		Version = previousVersion
		minAgentVersionByTaskKind["node.listener.apply"] = previousListenerMin
	}()

	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()

	candidates := []networking.Candidate{{Address: "10.0.0.93", Interface: "eth0", Kind: networking.KindLAN}}
	caps := testRuntimeCapabilities(NodeCapabilities{Docker: true, HostProfile: true})
	node := enrollOrchestrationNode(t, store, "mixed-compat-node", caps, candidates, networking.Profile{ServiceAddress: "10.0.0.93", LANAddress: "10.0.0.93", EnabledKinds: []string{networking.KindLAN}})
	heartbeat := func(version string) {
		t.Helper()
		if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{
			Version:                      version,
			Roles:                        []string{"worker"},
			Capabilities:                 caps,
			NetworkCandidates:            candidates,
			ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration,
			RemoteUpdateSupported:        true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	heartbeat("0.2.3")

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.queueNodeListenerState(ctx, tx, node.ID, store.now().UTC()); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := store.StartNodeDiagnostic(ctx, node.ID, nodediagnostics.HostProfileKind); err != nil {
		t.Fatal(err)
	}

	// Higher-priority node.listener.apply requires 0.4.0, so 0.2.3 must skip it
	// cleanly and still claim the compatible diagnostic task.
	task, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil || task == nil || task.Kind != nodediagnostics.HostProfileKind {
		t.Fatalf("expected compatible diagnostic claim while listener task waits: %#v, %v", task, err)
	}
	var status string
	var attempt int64
	if err := store.db.QueryRow(`SELECT status, attempt FROM node_listener_states WHERE node_id = ?`, node.ID).Scan(&status, &attempt); err != nil || status != "pending" || attempt != 0 {
		t.Fatalf("incompatible listener task was mutated: status=%q attempt=%d err=%v", status, attempt, err)
	}

	// With only the incompatible listener task left, claiming returns nil without error.
	if task, err := store.ClaimNextTask(ctx, node.ID, node.Credential); err != nil || task != nil {
		t.Fatalf("incompatible Agent claimed or errored on listener task: %#v, %v", task, err)
	}

	// Once upgraded to 0.4.0, the pending listener task is claimed immediately.
	heartbeat("0.4.0")
	task, err = store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil || task == nil || task.Kind != "node.listener.apply" {
		t.Fatalf("upgraded Agent did not claim listener task: %#v, %v", task, err)
	}
}

func TestDiagnosticClaimHonorsPerDiagnosticKindMinimumVersion(t *testing.T) {
	previousVersion := Version
	previousHostProfileMin := minAgentVersionByTaskKind[nodediagnostics.HostProfileKind]
	Version = "0.4.0"
	minAgentVersionByTaskKind[nodediagnostics.HostProfileKind] = "0.4.0"
	defer func() {
		Version = previousVersion
		minAgentVersionByTaskKind[nodediagnostics.HostProfileKind] = previousHostProfileMin
	}()

	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()

	candidates := []networking.Candidate{{Address: "10.0.0.94", Interface: "eth0", Kind: networking.KindLAN}}
	caps := testRuntimeCapabilities(NodeCapabilities{Docker: true, HostProfile: true, NetworkDiagnostics: true})
	node := enrollOrchestrationNode(t, store, "diag-compat-node", caps, candidates, networking.Profile{ServiceAddress: "10.0.0.94", LANAddress: "10.0.0.94", EnabledKinds: []string{networking.KindLAN}})
	heartbeat := func(version string) {
		t.Helper()
		if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{
			Version:                      version,
			Roles:                        []string{"worker"},
			Capabilities:                 caps,
			NetworkCandidates:            candidates,
			ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration,
			RemoteUpdateSupported:        true,
			PublicEgress: &networking.PublicEgress{
				Address:     "198.51.100.14",
				BindAddress: "10.0.0.94",
				Mode:        networking.PublicModeNAT,
				ObservedAt:  store.now().UTC(),
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	heartbeat("0.2.3")

	if err := store.StartNodeDiagnostic(ctx, node.ID, nodediagnostics.HostProfileKind); err != nil {
		t.Fatal(err)
	}
	if err := store.StartNodeDiagnostic(ctx, node.ID, nodediagnostics.NetworkKind); err != nil {
		t.Fatal(err)
	}

	task, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil || task == nil || task.Kind != nodediagnostics.NetworkKind {
		t.Fatalf("expected compatible network diagnostic claim: %#v, %v", task, err)
	}
	if task, err := store.ClaimNextTask(ctx, node.ID, node.Credential); err != nil || task != nil {
		t.Fatalf("incompatible host profile diagnostic was claimed: %#v, %v", task, err)
	}

	heartbeat("0.4.0")
	task, err = store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil || task == nil || task.Kind != nodediagnostics.HostProfileKind {
		t.Fatalf("upgraded Agent did not claim host profile diagnostic: %#v, %v", task, err)
	}
}

func TestReviewedPackageRetryBypassesRaisedApplicationApplyFloor(t *testing.T) {
	previousVersion := Version
	previousApplyMin := minAgentVersionByTaskKind["application.apply"]
	defer func() {
		Version = previousVersion
		minAgentVersionByTaskKind["application.apply"] = previousApplyMin
	}()

	Version = "0.2.3"
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()

	node := enrollOrchestrationNode(t, store, "reviewed-retry-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.95", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.95", LANAddress: "10.0.0.95", EnabledKinds: []string{networking.KindLAN}})
	installCPA(t, store, node, "10.0.0.95")
	heartbeatAgentUpdateVersion(t, store, node, "0.2.3", true)

	configure, err := store.CreateDeployment(ctx, DeploymentRequest{
		AgentID:                node.ID,
		AppKey:                 cpaAppKey,
		Operation:              "configure",
		Config:                 json.RawMessage(`{"debug":true}`),
		AuthorizedCapabilities: testCapabilityGrant("root"),
	})
	if err != nil {
		t.Fatal(err)
	}
	session := "reviewed-retry-current-session"
	if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	attempt1, err := store.claimExecutionTask(ctx, node.ID, node.Credential, session, 0)
	if err != nil || attempt1 == nil || attempt1.ID != configure.ID || attempt1.Attempt != 1 {
		t.Fatalf("claim attempt 1: %#v, %v", attempt1, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE task_executions SET state='failed', phase='reported', disposition='reexecute' WHERE id=?`, attempt1.Authorization.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET state='pending', lease_expires_at='' WHERE id=?`, configure.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE agents SET runtime_recovery='package-recovery-pending' WHERE id=?`, node.ID); err != nil {
		t.Fatal(err)
	}

	// Simulate a future Center release that raises application.apply's floor.
	Version = "0.4.0"
	minAgentVersionByTaskKind["application.apply"] = "0.4.0"

	// Because runtime_recovery is active, Agent updates cannot proceed yet.
	if _, err := store.QueueAgentUpdate(ctx, node.ID, Version); err == nil || !strings.Contains(err.Error(), "runtime recovery") {
		t.Fatalf("expected runtime recovery to block Agent update: %v", err)
	}

	// A pre-baseline (< 0.2.0) Agent must stay fenced even for a reviewed retry.
	if _, err := store.db.ExecContext(ctx, `UPDATE agents SET version='0.1.0' WHERE id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	if task, err := store.claimExecutionTask(ctx, node.ID, node.Credential, session, 0); err != nil || task != nil {
		t.Fatalf("pre-baseline Agent claimed reviewed retry: %#v, %v", task, err)
	}

	// A baseline-compatible (0.2.3) Agent must be able to claim its exact
	// reviewed retry so runtime recovery can finish before updating to 0.4.0.
	if _, err := store.db.ExecContext(ctx, `UPDATE agents SET version='0.2.3' WHERE id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	attempt2, err := store.claimExecutionTask(ctx, node.ID, node.Credential, session, 0)
	if err != nil || attempt2 == nil || attempt2.ID != configure.ID || attempt2.Attempt != 2 || attempt2.PackageRecovery == nil || attempt2.PackageRecovery.ExecutionID != attempt1.Authorization.ID || attempt2.PackageRecovery.ReceiptSHA256 != "" {
		t.Fatalf("baseline-compatible Agent did not claim reviewed retry: %#v, %v", attempt2, err)
	}

	// Fresh ordinary deployments on 0.2.3 remain fenced behind the 0.4.0 floor.
	fresh := enrollOrchestrationNode(t, store, "fresh-fenced-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.96", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.96", LANAddress: "10.0.0.96", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, fresh, "0.2.3", true)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{
		AgentID:                fresh.ID,
		AppKey:                 cpaAppKey,
		Config:                 json.RawMessage(`{"debug":false}`),
		AuthorizedCapabilities: testCapabilityGrant("root"),
	}); err != nil {
		t.Fatal(err)
	}
	if task, err := store.ClaimNextTask(ctx, fresh.ID, fresh.Credential); err != nil || task != nil {
		t.Fatalf("ordinary deployment bypassed raised application.apply floor: %#v, %v", task, err)
	}
}

func TestCommitTaskDefenseInDepthRejectsIncompatibleVersion(t *testing.T) {
	previousVersion := Version
	Version = "0.3.1"
	defer func() { Version = previousVersion }()

	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()

	node := enrollOrchestrationNode(t, store, "commit-fence-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.97", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.97", LANAddress: "10.0.0.97", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.2.3", true)

	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{
		AgentID:                node.ID,
		AppKey:                 cpaAppKey,
		Config:                 json.RawMessage(`{"debug":false}`),
		AuthorizedCapabilities: testCapabilityGrant("root"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER downgrade_agent_before_commit AFTER UPDATE OF state ON deployments WHEN NEW.state='running' BEGIN UPDATE agents SET version='0.1.0' WHERE id=NEW.agent_id; END`); err != nil {
		t.Fatal(err)
	}

	task, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err == nil || task != nil || !strings.Contains(err.Error(), "compatibility requirement") {
		t.Fatalf("commitTask did not reject incompatible version: task=%#v err=%v", task, err)
	}

	var state, version string
	var attempt int64
	if err := store.db.QueryRowContext(ctx, `SELECT d.state, d.attempt, a.version FROM deployments d JOIN agents a ON a.id=d.agent_id WHERE d.id=?`, deployment.ID).Scan(&state, &attempt, &version); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || attempt != 0 || version != "0.2.3" {
		t.Fatalf("failed compatibility commit did not roll back: state=%q attempt=%d version=%q", state, attempt, version)
	}
}

func TestCompatibleOlderAgentClaimsReinstallRecoveryTask(t *testing.T) {
	for _, tc := range []struct {
		name         string
		agentVersion string
		wantClaimed  bool
	}{
		{name: "compatible-older-agent", agentVersion: "0.2.3", wantClaimed: true},
		{name: "incompatible-legacy-agent", agentVersion: "0.1.0", wantClaimed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previousVersion := Version
			Version = "0.3.1"
			defer func() { Version = previousVersion }()

			s, _, node, input := monitorRestoreFixture(t)
			defer s.Close()
			ctx := context.Background()

			receipt, err := s.QueueAgentReinstallMonitorRestore(ctx, node.ID, "reinstall-review-admin", input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE agents SET version = ? WHERE id = ?`, tc.agentVersion, node.ID); err != nil {
				t.Fatal(err)
			}

			task, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "monitor-original-restore-session", 0)
			if tc.wantClaimed {
				if err != nil || task == nil || task.ID != receipt.DeploymentID {
					t.Fatalf("compatible %s Agent did not claim reinstall recovery task: %#v, %v", tc.agentVersion, task, err)
				}
				return
			}
			if task != nil || !errors.Is(err, errExecutionBlocked) {
				t.Fatalf("incompatible %s Agent claimed reinstall recovery task: %#v, %v", tc.agentVersion, task, err)
			}
		})
	}
}
