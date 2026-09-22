package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/meridianruntime"
)

func meridianRecoveryArtifact(revision uint64, config string) meridian.DesiredArtifact {
	digest := sha256.Sum256([]byte(config))
	return meridian.DesiredArtifact{Revision: revision, Config: []byte(config), ConfigSHA256: hex.EncodeToString(digest[:])}
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
