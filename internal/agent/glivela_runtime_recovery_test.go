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
	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/glivelaruntime"
	"github.com/petauron/vastora/internal/landing"
)

func glivelaRecoveryArtifact(revision uint64, config string) glivela.DesiredArtifact {
	digest := sha256.Sum256([]byte(config))
	return glivela.DesiredArtifact{Revision: revision, Config: []byte(config), ConfigSHA256: hex.EncodeToString(digest[:])}
}

func TestGlivelaRevisionOnlyKeepsRuntimeGateAndFreshReceipt(t *testing.T) {
	store := openGlivelaLandingTestStore(t)
	ctx := context.Background()
	state := glivelaLandingFixture()
	if err := store.saveGlivelaRuntimeState(ctx, state); err != nil {
		t.Fatal(err)
	}
	peer := state.AppliedPeers[0]
	checkedAt := time.Now().UTC().Add(-time.Second)
	status := landing.MonitorStatus{Revision: state.Applied.Revision, State: "healthy", LinkState: "direct", TCP: true, ExitIP: "1.1.1.1", StartedAt: checkedAt.Add(-time.Millisecond), CheckedAt: checkedAt, Stage: "startup", Reason: "startup_ready"}
	store.landingDone = make(chan struct{})
	store.glivelaMonitorRevision, store.glivelaMonitorSHA256 = state.Applied.Revision, state.Applied.ConfigSHA256
	store.glivelaMonitorSource = cloneGlivelaSource(state.AppliedSource)
	store.glivelaPeerStatuses = map[string]glivelaruntime.PeerObservation{peer.EgressID: {EgressID: peer.EgressID, Identity: peer.Identity, Status: status}}
	task := glivelaruntime.Task{ApplicationID: state.ApplicationID, ImageReference: state.ImageReference, Desired: *state.Applied, Peers: state.AppliedPeers, Source: state.AppliedSource}
	task.Desired.Revision++
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	var observed []uint64
	observe := func(_ context.Context, current glivelaRuntimeState) (glivelaruntime.Result, error) {
		observed = append(observed, current.Applied.Revision)
		return glivelaruntime.Result{
			Receipt: glivela.AppliedReceipt{Revision: current.Applied.Revision, ConfigSHA256: current.Applied.ConfigSHA256, RuntimeReady: true},
			Stats:   json.RawMessage(`{"stat":[]}`), Peers: store.glivelaPeerObservations(current), Source: cloneGlivelaSource(current.AppliedSource),
		}, nil
	}
	result, err := (ApplicationExecutor{Store: store}).advanceGlivelaRevision(ctx, state, task, observe)
	if err != nil || result.Validate(task.Desired) != nil || !reflect.DeepEqual(observed, []uint64{7, 8}) {
		t.Fatalf("revision-only receipt=%#v observed=%v err=%v", result.Receipt, observed, err)
	}
	if health, err := result.PeerHealth(task, time.Now().UTC()); err != nil || !health[peer.EgressID] || result.Peers[0].Status.CheckedAt != checkedAt {
		t.Fatalf("unchanged peer evidence was lost: health=%v result=%#v err=%v", health, result.Peers, err)
	}
	persisted, err := store.loadGlivelaRuntimeState(ctx)
	if err != nil || persisted.Applied.Revision != 8 || persisted.GateRevision != 7 || persisted.knownLandingGates()[0].Revision != 7 || !store.glivelaLandingMonitorRunning(persisted) {
		t.Fatalf("no-reload journal or monitor changed: state=%#v err=%v", persisted, err)
	}
	if store.glivelaPeerStatuses[peer.EgressID].Status.Revision != 7 {
		t.Fatal("revision-only receipt mutated the gate monitor's original evidence")
	}
	// A second metadata revision keeps the same gate across repeated no-op
	// updates rather than creating a fresh nft table for each account edit.
	task.Desired.Revision++
	if !canAdvanceGlivelaRevision(persisted, task) || persisted.appliedGateRevision() != 7 {
		t.Fatal("another identical revision would replace the existing gate")
	}
}

func TestGlivelaRevisionOnlyRequiresUnchangedVerifiedAuthority(t *testing.T) {
	base := glivelaLandingFixture()
	task := glivelaruntime.Task{ApplicationID: base.ApplicationID, ImageReference: base.ImageReference, Desired: *base.Applied, Peers: base.AppliedPeers, Source: base.AppliedSource}
	task.Desired.Revision++
	if !canAdvanceGlivelaRevision(base, task) {
		t.Fatal("identical higher revision cannot use the metadata path")
	}
	for _, test := range []struct {
		name   string
		change func(*glivelaRuntimeState, *glivelaruntime.Task)
	}{
		{"changed config", func(_ *glivelaRuntimeState, task *glivelaruntime.Task) {
			task.Desired = glivelaRecoveryArtifact(8, `{"outbounds":[]}`)
		}},
		{"changed peer", func(_ *glivelaRuntimeState, task *glivelaruntime.Task) {
			task.Peers = append([]glivelaruntime.Peer(nil), task.Peers...)
			task.Peers[0].Identity.PublicKey = "nodekey:other"
		}},
		{"changed source", func(_ *glivelaRuntimeState, task *glivelaruntime.Task) {
			task.Source = &landing.PeerIdentity{ID: "other", PublicKey: "nodekey:other", Address: "100.64.0.1"}
		}},
		{"pending replacement", func(state *glivelaRuntimeState, _ *glivelaruntime.Task) {
			pending := glivelaRecoveryArtifact(8, `{"outbounds":[]}`)
			state.Pending = &pending
		}},
		{"pending handover", func(state *glivelaRuntimeState, _ *glivelaruntime.Task) { state.HandoverPending = true }},
		{"retiring gate", func(state *glivelaRuntimeState, _ *glivelaruntime.Task) {
			state.RetiringGates = state.knownLandingGates()
		}},
		{"recovery", func(_ *glivelaRuntimeState, task *glivelaruntime.Task) { task.ReplacePendingState = true }},
		{"legacy retirement", func(_ *glivelaRuntimeState, task *glivelaruntime.Task) { task.RetireLegacy = true }},
		{"stale revision", func(_ *glivelaRuntimeState, task *glivelaruntime.Task) { task.Desired.Revision-- }},
		{"image change", func(_ *glivelaRuntimeState, task *glivelaruntime.Task) { task.ImageReference = "different-image" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, candidate := base, task
			test.change(&state, &candidate)
			if canAdvanceGlivelaRevision(state, candidate) {
				t.Fatal("unsafe revision used the metadata path")
			}
		})
	}
}

func TestGlivelaRevisionOnlyDoesNotJournalUnverifiedRuntime(t *testing.T) {
	store := openGlivelaLandingTestStore(t)
	ctx := context.Background()
	state := glivelaLandingFixture()
	if err := store.saveGlivelaRuntimeState(ctx, state); err != nil {
		t.Fatal(err)
	}
	task := glivelaruntime.Task{ApplicationID: state.ApplicationID, ImageReference: state.ImageReference, Desired: *state.Applied, Peers: state.AppliedPeers, Source: state.AppliedSource}
	task.Desired.Revision++
	if _, err := (ApplicationExecutor{Store: store}).advanceGlivelaRevision(ctx, state, task, func(context.Context, glivelaRuntimeState) (glivelaruntime.Result, error) {
		return glivelaruntime.Result{}, errors.New("container configuration changed")
	}); err == nil {
		t.Fatal("unverified runtime advanced its receipt")
	}
	persisted, err := store.loadGlivelaRuntimeState(ctx)
	if err != nil || persisted.Applied.Revision != state.Applied.Revision || persisted.GateRevision != 0 {
		t.Fatalf("failed verification changed journal: %#v err=%v", persisted, err)
	}
}

func TestReplaceGlivelaPendingStateRequiresExplicitNewerCenterRevision(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	applied := glivelaRecoveryArtifact(1, `{"revision":1}`)
	pending := glivelaRecoveryArtifact(2, `{"revision":2}`)
	state := glivelaRuntimeState{ApplicationID: "meridian-application", ImageReference: xrayWorkerImageReference, Applied: &applied, Pending: &pending}
	if err := store.saveGlivelaRuntimeState(ctx, state); err != nil {
		t.Fatal(err)
	}
	executor := ApplicationExecutor{Store: store}
	desired := glivelaRecoveryArtifact(3, `{"revision":3}`)
	task := glivelaruntime.Task{ApplicationID: state.ApplicationID, ImageReference: xrayWorkerImageReference, Desired: desired}
	if _, err := executor.replaceGlivelaPendingState(ctx, state, task, 1001); err == nil {
		t.Fatal("pending state changed without explicit Center authorization")
	}
	task.ReplacePendingState = true
	replaced, err := executor.replaceGlivelaPendingState(ctx, state, task, 1001)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Pending == nil || replaced.Pending.Revision != desired.Revision || replaced.Pending.ConfigSHA256 != desired.ConfigSHA256 || replaced.Applied == nil || replaced.Applied.Revision != applied.Revision {
		t.Fatalf("replaced Glivela state=%#v", replaced)
	}
	persisted, err := store.loadGlivelaRuntimeState(ctx)
	if err != nil || persisted.Pending == nil || persisted.Pending.Revision != desired.Revision {
		t.Fatalf("persisted Glivela state=%#v err=%v", persisted, err)
	}
	staleTask := task
	staleTask.Desired = glivelaRecoveryArtifact(1, `{"revision":"stale"}`)
	if _, err := executor.replaceGlivelaPendingState(ctx, persisted, staleTask, 1001); err == nil {
		t.Fatal("explicit recovery accepted a revision at or below the applied receipt")
	}
}

func TestCompletedGlivelaReplacementRemovesOnlyStoppedOwnedResidue(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	artifact := glivelaRecoveryArtifact(2, `{"inbounds":[]}`)
	state := glivelaRuntimeState{ApplicationID: threeXUITestApplicationID, ImageReference: xrayWorkerImageReference, Applied: &artifact}
	directory := filepath.Join(store.dataDir, glivelaRuntimeDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "config.json"), artifact.Config, 0o600); err != nil {
		t.Fatal(err)
	}
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("current", legacyXrayRuntimeContainer, true)
	engine.containers["current"].labels = applicationResourceLabels(legacyProxyAppKey, "xray", state.ApplicationID, "meridian-runtime-r2")
	engine.containers["current"].labels[xrayWorkerRuntimeLabel] = "xray"
	engine.containers["current"].image = xrayWorkerImageReference
	engine.add("legacy-residue", glivelaXrayBackupContainer, false)
	executor := ApplicationExecutor{Store: store}
	if err := executor.cleanupCompletedGlivelaReplacement(context.Background(), engine, state); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.resolve(glivelaXrayBackupContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("verified legacy residue was retained: %v", err)
	}
	if current, err := engine.resolve(legacyXrayRuntimeContainer); err != nil || !current.running {
		t.Fatalf("current Glivela runtime changed: %#v err=%v", current, err)
	}
}
