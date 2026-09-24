package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
)

func meridianRecoveryArtifact(revision uint64, config string) meridian.DesiredArtifact {
	digest := sha256.Sum256([]byte(config))
	return meridian.DesiredArtifact{Revision: revision, Config: []byte(config), ConfigSHA256: hex.EncodeToString(digest[:])}
}

func TestMeridianRevisionOnlyKeepsRuntimeGateAndFreshReceipt(t *testing.T) {
	store := openMeridianLandingTestStore(t)
	ctx := context.Background()
	state := meridianLandingFixture()
	if err := store.saveMeridianRuntimeState(ctx, state); err != nil {
		t.Fatal(err)
	}
	peer := state.AppliedPeers[0]
	checkedAt := time.Now().UTC().Add(-time.Second)
	status := landing.MonitorStatus{Revision: state.Applied.Revision, State: "healthy", LinkState: "direct", TCP: true, ExitIPv4: "1.1.1.1", CheckedAt: checkedAt, AllowedUntil: checkedAt.Add(landing.AllowLifetime)}
	store.landingDone = make(chan struct{})
	store.meridianMonitorRevision, store.meridianMonitorSHA256 = state.Applied.Revision, state.Applied.ConfigSHA256
	store.meridianMonitorSource = cloneMeridianSource(state.AppliedSource)
	store.meridianPeerStatuses = map[string]meridianruntime.PeerObservation{peer.EgressID: {EgressID: peer.EgressID, Identity: peer.Identity, Status: status}}
	task := meridianruntime.Task{ApplicationID: state.ApplicationID, ImageReference: state.ImageReference, Desired: *state.Applied, Peers: state.AppliedPeers, Source: state.AppliedSource}
	task.Desired.Revision++
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	var observed []uint64
	observe := func(_ context.Context, current meridianRuntimeState) (meridianruntime.Result, error) {
		observed = append(observed, current.Applied.Revision)
		return meridianruntime.Result{
			Receipt: meridian.AppliedReceipt{Revision: current.Applied.Revision, ConfigSHA256: current.Applied.ConfigSHA256, RuntimeReady: true},
			Stats:   json.RawMessage(`{"stat":[]}`), Peers: store.meridianPeerObservations(current), Source: cloneMeridianSource(current.AppliedSource),
		}, nil
	}
	result, err := (ApplicationExecutor{Store: store}).advanceMeridianRevision(ctx, state, task, observe)
	if err != nil || result.Validate(task.Desired) != nil || !reflect.DeepEqual(observed, []uint64{7, 8}) {
		t.Fatalf("revision-only receipt=%#v observed=%v err=%v", result.Receipt, observed, err)
	}
	if health, err := result.PeerHealth(task, time.Now().UTC()); err != nil || !health[peer.EgressID] || result.Peers[0].Status.CheckedAt != checkedAt {
		t.Fatalf("unchanged peer evidence was lost: health=%v result=%#v err=%v", health, result.Peers, err)
	}
	persisted, err := store.loadMeridianRuntimeState(ctx)
	if err != nil || persisted.Applied.Revision != 8 || persisted.GateRevision != 7 || persisted.knownLandingGates()[0].Revision != 7 || !store.meridianLandingMonitorRunning(persisted) {
		t.Fatalf("no-reload journal or monitor changed: state=%#v err=%v", persisted, err)
	}
	if store.meridianPeerStatuses[peer.EgressID].Status.Revision != 7 {
		t.Fatal("revision-only receipt mutated the gate monitor's original evidence")
	}
	// A second metadata revision keeps the same gate across repeated no-op
	// updates rather than creating a fresh nft table for each account edit.
	task.Desired.Revision++
	if !canAdvanceMeridianRevision(persisted, task) || persisted.appliedGateRevision() != 7 {
		t.Fatal("another identical revision would replace the existing gate")
	}
}

func TestMeridianRevisionOnlyRequiresUnchangedVerifiedAuthority(t *testing.T) {
	base := meridianLandingFixture()
	task := meridianruntime.Task{ApplicationID: base.ApplicationID, ImageReference: base.ImageReference, Desired: *base.Applied, Peers: base.AppliedPeers, Source: base.AppliedSource}
	task.Desired.Revision++
	if !canAdvanceMeridianRevision(base, task) {
		t.Fatal("identical higher revision cannot use the metadata path")
	}
	for _, test := range []struct {
		name   string
		change func(*meridianRuntimeState, *meridianruntime.Task)
	}{
		{"changed config", func(_ *meridianRuntimeState, task *meridianruntime.Task) {
			task.Desired = meridianRecoveryArtifact(8, `{"outbounds":[]}`)
		}},
		{"changed peer", func(_ *meridianRuntimeState, task *meridianruntime.Task) {
			task.Peers = append([]meridianruntime.Peer(nil), task.Peers...)
			task.Peers[0].Identity.PublicKey = "nodekey:other"
		}},
		{"changed source", func(_ *meridianRuntimeState, task *meridianruntime.Task) {
			task.Source = &landing.PeerIdentity{ID: "other", PublicKey: "nodekey:other", Address: "100.64.0.1"}
		}},
		{"pending replacement", func(state *meridianRuntimeState, _ *meridianruntime.Task) {
			pending := meridianRecoveryArtifact(8, `{"outbounds":[]}`)
			state.Pending = &pending
		}},
		{"pending handover", func(state *meridianRuntimeState, _ *meridianruntime.Task) { state.HandoverPending = true }},
		{"retiring gate", func(state *meridianRuntimeState, _ *meridianruntime.Task) {
			state.RetiringGates = state.knownLandingGates()
		}},
		{"recovery", func(_ *meridianRuntimeState, task *meridianruntime.Task) { task.ReplacePendingState = true }},
		{"legacy retirement", func(_ *meridianRuntimeState, task *meridianruntime.Task) { task.RetireLegacy = true }},
		{"stale revision", func(_ *meridianRuntimeState, task *meridianruntime.Task) { task.Desired.Revision-- }},
		{"image change", func(_ *meridianRuntimeState, task *meridianruntime.Task) { task.ImageReference = "different-image" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, candidate := base, task
			test.change(&state, &candidate)
			if canAdvanceMeridianRevision(state, candidate) {
				t.Fatal("unsafe revision used the metadata path")
			}
		})
	}
}

func TestMeridianRevisionOnlyDoesNotJournalUnverifiedRuntime(t *testing.T) {
	store := openMeridianLandingTestStore(t)
	ctx := context.Background()
	state := meridianLandingFixture()
	if err := store.saveMeridianRuntimeState(ctx, state); err != nil {
		t.Fatal(err)
	}
	task := meridianruntime.Task{ApplicationID: state.ApplicationID, ImageReference: state.ImageReference, Desired: *state.Applied, Peers: state.AppliedPeers, Source: state.AppliedSource}
	task.Desired.Revision++
	if _, err := (ApplicationExecutor{Store: store}).advanceMeridianRevision(ctx, state, task, func(context.Context, meridianRuntimeState) (meridianruntime.Result, error) {
		return meridianruntime.Result{}, errors.New("container configuration changed")
	}); err == nil {
		t.Fatal("unverified runtime advanced its receipt")
	}
	persisted, err := store.loadMeridianRuntimeState(ctx)
	if err != nil || persisted.Applied.Revision != state.Applied.Revision || persisted.GateRevision != 0 {
		t.Fatalf("failed verification changed journal: %#v err=%v", persisted, err)
	}
}

func TestReplaceMeridianPendingStateRequiresExplicitNewerCenterRevision(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	applied := meridianRecoveryArtifact(1, `{"revision":1}`)
	pending := meridianRecoveryArtifact(2, `{"revision":2}`)
	state := meridianRuntimeState{ApplicationID: "meridian-application", ImageReference: xrayWorkerImageReference, Applied: &applied, Pending: &pending}
	if err := store.saveMeridianRuntimeState(ctx, state); err != nil {
		t.Fatal(err)
	}
	executor := ApplicationExecutor{Store: store}
	desired := meridianRecoveryArtifact(3, `{"revision":3}`)
	task := meridianruntime.Task{ApplicationID: state.ApplicationID, ImageReference: xrayWorkerImageReference, Desired: desired}
	if _, err := executor.replaceMeridianPendingState(ctx, state, task); err == nil {
		t.Fatal("pending state changed without explicit Center authorization")
	}
	task.ReplacePendingState = true
	replaced, err := executor.replaceMeridianPendingState(ctx, state, task)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Pending == nil || replaced.Pending.Revision != desired.Revision || replaced.Pending.ConfigSHA256 != desired.ConfigSHA256 || replaced.Applied == nil || replaced.Applied.Revision != applied.Revision {
		t.Fatalf("replaced Meridian state=%#v", replaced)
	}
	persisted, err := store.loadMeridianRuntimeState(ctx)
	if err != nil || persisted.Pending == nil || persisted.Pending.Revision != desired.Revision {
		t.Fatalf("persisted Meridian state=%#v err=%v", persisted, err)
	}
	staleTask := task
	staleTask.Desired = meridianRecoveryArtifact(1, `{"revision":"stale"}`)
	if _, err := executor.replaceMeridianPendingState(ctx, persisted, staleTask); err == nil {
		t.Fatal("explicit recovery accepted a revision at or below the applied receipt")
	}
}

func TestCompletedMeridianReplacementRemovesOnlyStoppedOwnedResidue(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	artifact := meridianRecoveryArtifact(2, `{"inbounds":[]}`)
	state := meridianRuntimeState{ApplicationID: threeXUITestApplicationID, ImageReference: xrayWorkerImageReference, Applied: &artifact}
	directory := filepath.Join(store.dataDir, meridianRuntimeDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "config.json"), artifact.Config, 0o600); err != nil {
		t.Fatal(err)
	}
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("current", meridianXrayContainer, true)
	engine.containers["current"].labels = applicationResourceLabels(meridianKey, "xray", state.ApplicationID, "meridian-runtime-r2")
	engine.containers["current"].labels[xrayWorkerRuntimeLabel] = "xray"
	engine.containers["current"].image = xrayWorkerImageReference
	engine.add("legacy-residue", meridianXrayBackupContainer, false)
	executor := ApplicationExecutor{Store: store}
	if err := executor.cleanupCompletedMeridianReplacement(context.Background(), engine, state); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.resolve(meridianXrayBackupContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("verified legacy residue was retained: %v", err)
	}
	if current, err := engine.resolve(meridianXrayContainer); err != nil || !current.running {
		t.Fatalf("current Meridian runtime changed: %#v err=%v", current, err)
	}
}

func TestMeridianUninstallRemovesVerifiedLegacyReplacementResidue(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("current", meridianXrayContainer, true)
	engine.containers["current"].labels = applicationResourceLabels(meridianKey, "xray", threeXUITestApplicationID, "meridian-runtime-r2")
	engine.containers["current"].labels[xrayWorkerRuntimeLabel] = "xray"
	engine.containers["current"].image = xrayWorkerImageReference
	engine.add("legacy-residue", meridianXrayCleanupContainer, false)
	if err := uninstallDockerApp(context.Background(), engine, meridianKey, threeXUITestApplicationID, true); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{meridianXrayContainer, meridianXrayCleanupContainer} {
		if _, err := engine.resolve(name); !errdefs.IsNotFound(err) {
			t.Fatalf("Meridian uninstall retained %s: %v", name, err)
		}
	}
}
