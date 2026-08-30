package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"strings"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

func TestThreeXUIPortsPublishOnlySelectedServices(t *testing.T) {
	exposed, bindings, err := threeXUIPorts("100.64.0.10", 2053, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := exposed[dockernetwork.MustParsePort("2096/tcp")]; exists {
		t.Fatal("worker unexpectedly publishes the master subscription service")
	}
	for _, number := range []string{"2053/tcp", "20000/tcp", "20031/tcp"} {
		port := dockernetwork.MustParsePort(number)
		if _, exists := exposed[port]; !exists || len(bindings[port]) != 1 || bindings[port][0].HostIP.String() != "100.64.0.10" {
			t.Fatalf("3x-ui mapping %s is missing", number)
		}
	}
}

func TestThreeXUIPortsRejectPublicOnlyServiceAddress(t *testing.T) {
	if _, _, err := threeXUIPorts("198.51.100.10", 2053, "master"); err == nil {
		t.Fatalf("public-only 3x-ui binding was accepted: %v", err)
	}
}

type fakeThreeXUIContainer struct {
	id      string
	name    string
	running bool
	labels  map[string]string
}

const threeXUITestApplicationID = "application-1"

func threeXUITestLabels(deploymentID string) map[string]string {
	return applicationResourceLabels(threeXUIKey, "3x-ui", threeXUITestApplicationID, deploymentID)
}

func threeXUITestCreateOptions(deploymentID string) client.ContainerCreateOptions {
	return client.ContainerCreateOptions{Name: threeXUICandidateContainer, Config: &container.Config{Image: "3x-ui:test", Labels: threeXUITestLabels(deploymentID)}}
}

func (engine *fakeThreeXUIContainerEngine) setVolumeState(id, state string) {
	engine.containers[id].labels = threeXUITestLabels("deployment-1")
	engine.containers[id].labels[threeXUIVolumeStateLabel] = state
}

type fakeThreeXUIContainerEngine struct {
	containers          map[string]*fakeThreeXUIContainer
	names               map[string]string
	nextID              int
	failCreate          bool
	failStartName       string
	failStopName        string
	failStopAfter       string
	failRenameName      string
	failRenameAfterName string
	failRemoveName      string
	failVolumeRemove    bool
	startCalls          int
	volumeExists        bool
	volumeLabels        map[string]string
	removedVolumes      []string
	snapshot            []byte
	persisted           []byte
	restored            []byte
}

func newFakeThreeXUIContainerEngine(t *testing.T, withCurrent bool) *fakeThreeXUIContainerEngine {
	t.Helper()
	engine := &fakeThreeXUIContainerEngine{
		containers: map[string]*fakeThreeXUIContainer{}, names: map[string]string{}, snapshot: threeXUITestDatabaseArchive(t), volumeExists: withCurrent,
		volumeLabels: applicationResourceLabels(threeXUIKey, applicationVolumeComponent(threeXUIDatabaseVolume), threeXUITestApplicationID, ""),
	}
	if withCurrent {
		engine.add("old", threeXUIContainer, true)
	}
	return engine
}

func (engine *fakeThreeXUIContainerEngine) add(id, name string, running bool) {
	engine.containers[id] = &fakeThreeXUIContainer{id: id, name: name, running: running, labels: threeXUITestLabels("deployment-1")}
	engine.names[name] = id
}

func (engine *fakeThreeXUIContainerEngine) resolve(value string) (*fakeThreeXUIContainer, error) {
	id := value
	if named, ok := engine.names[value]; ok {
		id = named
	}
	entry, ok := engine.containers[id]
	if !ok {
		return nil, errdefs.ErrNotFound.WithMessage("container not found")
	}
	return entry, nil
}

func (engine *fakeThreeXUIContainerEngine) ContainerCreate(_ context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	if engine.failCreate {
		return client.ContainerCreateResult{}, errors.New("create failed")
	}
	engine.nextID++
	id := "candidate-id"
	engine.add(id, options.Name, false)
	if options.Config != nil {
		engine.containers[id].labels = maps.Clone(options.Config.Labels)
	}
	engine.volumeExists = true
	return client.ContainerCreateResult{ID: id}, nil
}

func (engine *fakeThreeXUIContainerEngine) ContainerStart(_ context.Context, value string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
	engine.startCalls++
	entry, err := engine.resolve(value)
	if err != nil {
		return client.ContainerStartResult{}, err
	}
	if entry.name == engine.failStartName {
		return client.ContainerStartResult{}, errors.New("start failed")
	}
	if entry.running {
		return client.ContainerStartResult{}, errdefs.ErrNotModified
	}
	entry.running = true
	return client.ContainerStartResult{}, nil
}

func (engine *fakeThreeXUIContainerEngine) ContainerStop(_ context.Context, value string, _ client.ContainerStopOptions) (client.ContainerStopResult, error) {
	entry, err := engine.resolve(value)
	if err != nil {
		return client.ContainerStopResult{}, err
	}
	if entry.name == engine.failStopName {
		return client.ContainerStopResult{}, errors.New("stop failed")
	}
	if entry.name == engine.failStopAfter {
		entry.running = false
		return client.ContainerStopResult{}, errors.New("stop response lost")
	}
	if !entry.running {
		return client.ContainerStopResult{}, errdefs.ErrNotModified
	}
	entry.running = false
	return client.ContainerStopResult{}, nil
}

func (engine *fakeThreeXUIContainerEngine) ContainerRemove(_ context.Context, value string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	entry, err := engine.resolve(value)
	if err != nil {
		return client.ContainerRemoveResult{}, err
	}
	if entry.name == engine.failRemoveName {
		return client.ContainerRemoveResult{}, errors.New("remove failed")
	}
	delete(engine.names, entry.name)
	delete(engine.containers, entry.id)
	return client.ContainerRemoveResult{}, nil
}

func (engine *fakeThreeXUIContainerEngine) ContainerRename(_ context.Context, value string, options client.ContainerRenameOptions) (client.ContainerRenameResult, error) {
	entry, err := engine.resolve(value)
	if err != nil {
		return client.ContainerRenameResult{}, err
	}
	if options.NewName == engine.failRenameName {
		return client.ContainerRenameResult{}, errors.New("rename failed")
	}
	if _, exists := engine.names[options.NewName]; exists {
		return client.ContainerRenameResult{}, errdefs.ErrAlreadyExists
	}
	delete(engine.names, entry.name)
	entry.name = options.NewName
	engine.names[entry.name] = entry.id
	if options.NewName == engine.failRenameAfterName {
		return client.ContainerRenameResult{}, errors.New("rename response lost")
	}
	return client.ContainerRenameResult{}, nil
}

func (engine *fakeThreeXUIContainerEngine) ContainerInspect(_ context.Context, value string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	entry, err := engine.resolve(value)
	if err != nil {
		return client.ContainerInspectResult{}, err
	}
	return client.ContainerInspectResult{Container: container.InspectResponse{ID: entry.id, Name: "/" + entry.name, State: &container.State{Running: entry.running}, Config: &container.Config{Labels: maps.Clone(entry.labels)}, HostConfig: &container.HostConfig{}}}, nil
}

func (engine *fakeThreeXUIContainerEngine) CopyFromContainer(_ context.Context, value string, options client.CopyFromContainerOptions) (client.CopyFromContainerResult, error) {
	if _, err := engine.resolve(value); err != nil {
		return client.CopyFromContainerResult{}, err
	}
	if options.SourcePath == threeXUIDurableSnapshot+"/x-ui" && len(engine.persisted) == 0 {
		return client.CopyFromContainerResult{}, errdefs.ErrNotFound.WithMessage("durable snapshot not found")
	}
	return client.CopyFromContainerResult{Content: io.NopCloser(bytes.NewReader(engine.snapshot))}, nil
}

func (engine *fakeThreeXUIContainerEngine) CopyToContainer(_ context.Context, value string, options client.CopyToContainerOptions) (client.CopyToContainerResult, error) {
	if _, err := engine.resolve(value); err != nil {
		return client.CopyToContainerResult{}, err
	}
	data, err := io.ReadAll(options.Content)
	if err != nil {
		return client.CopyToContainerResult{}, err
	}
	if options.DestinationPath == "/" {
		engine.persisted = data
	} else {
		engine.restored = data
	}
	return client.CopyToContainerResult{}, nil
}

func (engine *fakeThreeXUIContainerEngine) VolumeCreate(_ context.Context, options client.VolumeCreateOptions) (client.VolumeCreateResult, error) {
	engine.volumeExists = true
	engine.volumeLabels = maps.Clone(options.Labels)
	return client.VolumeCreateResult{Volume: volume.Volume{Name: options.Name, Labels: maps.Clone(options.Labels)}}, nil
}

func (engine *fakeThreeXUIContainerEngine) VolumeInspect(_ context.Context, _ string, _ client.VolumeInspectOptions) (client.VolumeInspectResult, error) {
	if !engine.volumeExists {
		return client.VolumeInspectResult{}, errdefs.ErrNotFound.WithMessage("volume not found")
	}
	return client.VolumeInspectResult{Volume: volume.Volume{Name: threeXUIDatabaseVolume, Labels: maps.Clone(engine.volumeLabels)}}, nil
}

func (engine *fakeThreeXUIContainerEngine) VolumeRemove(_ context.Context, name string, _ client.VolumeRemoveOptions) (client.VolumeRemoveResult, error) {
	engine.removedVolumes = append(engine.removedVolumes, name)
	if engine.failVolumeRemove {
		return client.VolumeRemoveResult{}, errors.New("volume remove failed")
	}
	if name == threeXUIDatabaseVolume {
		engine.volumeExists = false
	}
	return client.VolumeRemoveResult{}, nil
}

func acceptThreeXUIPromotion(string, string) error { return nil }

func TestOfflineThreeXUIRestoreCannotCreateFreshDatabase(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	_, err := replaceThreeXUIContainer(context.Background(), engine, threeXUITestCreateOptions("deployment-1"), false, func(string) (string, error) {
		return "", nil
	}, acceptThreeXUIPromotion)
	if err == nil || !strings.Contains(err.Error(), "cannot create a new 3x-ui database") {
		t.Fatalf("offline restore error = %v", err)
	}
	if engine.volumeExists || engine.startCalls != 0 {
		t.Fatal("offline restore created fresh 3x-ui state")
	}
}

func TestReplaceThreeXUIContainerRollsBackContainerAndDatabase(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, true)
	_, err := replaceThreeXUIContainer(context.Background(), engine, threeXUITestCreateOptions("deployment-1"), true, func(string) (string, error) {
		return "", errors.New("configuration failed")
	}, acceptThreeXUIPromotion)
	if err == nil || !strings.Contains(err.Error(), "configuration failed") {
		t.Fatalf("replace error = %v", err)
	}
	current, resolveErr := engine.resolve(threeXUIContainer)
	if resolveErr != nil || current.id != "old" || !current.running {
		t.Fatalf("previous container was not restored: %#v, err=%v", current, resolveErr)
	}
	if _, err := engine.resolve(threeXUICandidateContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("failed candidate was retained: %v", err)
	}
	if len(engine.persisted) == 0 || len(engine.restored) == 0 {
		t.Fatal("shared 3x-ui database was not durably snapshotted and restored before restarting the old container")
	}
}

func TestReplaceThreeXUIContainerPromotesOnlyHealthyCandidate(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, true)
	token, err := replaceThreeXUIContainer(context.Background(), engine, threeXUITestCreateOptions("deployment-1"), true, func(string) (string, error) {
		return "new-token", nil
	}, acceptThreeXUIPromotion)
	if err != nil || token != "new-token" {
		t.Fatalf("replace result token=%q err=%v", token, err)
	}
	current, resolveErr := engine.resolve(threeXUIContainer)
	if resolveErr != nil || current.id != "candidate-id" || !current.running {
		t.Fatalf("healthy candidate was not promoted: %#v, err=%v", current, resolveErr)
	}
	if current.labels[threeXUIDeploymentIDLabel] != "deployment-1" {
		t.Fatalf("promoted candidate lost deployment identity: %#v", current.labels)
	}
	if _, err := engine.resolve("old"); !errdefs.IsNotFound(err) {
		t.Fatalf("old container was not removed after promotion: %v", err)
	}
	if len(engine.restored) != 0 {
		t.Fatal("successful promotion unexpectedly restored the old database")
	}
}

func TestReplaceThreeXUIContainerKeepsRollbackUntilPostPromotionHealthPasses(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, true)
	_, err := replaceThreeXUIContainer(context.Background(), engine, threeXUITestCreateOptions("deployment-1"), true, func(string) (string, error) {
		return "new-token", nil
	}, func(containerID, _ string) error {
		promoted, resolveErr := engine.resolve(containerID)
		if resolveErr != nil {
			return resolveErr
		}
		promoted.running = false
		return errors.New("promoted service exited")
	})
	if err == nil || !strings.Contains(err.Error(), "promoted service exited") {
		t.Fatalf("post-promotion health error = %v", err)
	}
	current, resolveErr := engine.resolve(threeXUIContainer)
	if resolveErr != nil || current.id != "old" || !current.running {
		t.Fatalf("post-promotion failure lost the last known-good service: %#v err=%v", current, resolveErr)
	}
	if _, err := engine.resolve("candidate-id"); !errdefs.IsNotFound(err) {
		t.Fatalf("failed promoted candidate survived rollback: %v", err)
	}
	if len(engine.restored) == 0 {
		t.Fatal("post-promotion failure did not restore the durable database snapshot")
	}
}

func TestReplaceThreeXUIContainerRestoresRetainedVolumeAfterPostPromotionFailure(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.volumeExists = true
	_, err := replaceThreeXUIContainer(context.Background(), engine, threeXUITestCreateOptions("deployment-1"), true, func(string) (string, error) {
		return "new-token", nil
	}, func(string, string) error { return errors.New("promoted API unavailable") })
	if err == nil || !strings.Contains(err.Error(), "promoted API unavailable") {
		t.Fatalf("post-promotion verification error = %v", err)
	}
	if _, err := engine.resolve(threeXUIContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("failed retained-data reinstall left a live container: %v", err)
	}
	if !engine.volumeExists || len(engine.restored) == 0 {
		t.Fatalf("retained database was not restored: volume=%t restored=%d", engine.volumeExists, len(engine.restored))
	}
}

func TestReplaceThreeXUIContainerRemovesFreshVolumeAfterPostPromotionFailure(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	_, err := replaceThreeXUIContainer(context.Background(), engine, threeXUITestCreateOptions("deployment-1"), true, func(string) (string, error) {
		return "new-token", nil
	}, func(string, string) error { return errors.New("promoted API unavailable") })
	if err == nil {
		t.Fatal("fresh post-promotion verification unexpectedly succeeded")
	}
	if _, err := engine.resolve(threeXUIContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("failed fresh install left a live container: %v", err)
	}
	if engine.volumeExists {
		t.Fatal("failed fresh install retained its newly created database volume")
	}
}

func TestReplaceThreeXUIContainerTreatsLostPromotionResponseAsCommitted(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, true)
	engine.failRenameAfterName = threeXUIContainer
	token, err := replaceThreeXUIContainer(context.Background(), engine, threeXUITestCreateOptions("deployment-2"), true, func(string) (string, error) {
		return "new-token", nil
	}, acceptThreeXUIPromotion)
	if err != nil || token != "new-token" {
		t.Fatalf("lost promotion response was not reconciled: token=%q err=%v", token, err)
	}
	current, resolveErr := engine.resolve(threeXUIContainer)
	if resolveErr != nil || current.id != "candidate-id" || !current.running || current.labels[threeXUIDeploymentIDLabel] != "deployment-2" {
		t.Fatalf("promoted candidate was rolled back after a lost response: %#v err=%v", current, resolveErr)
	}
	if len(engine.restored) != 0 {
		t.Fatal("lost promotion response restored the old database after commit")
	}
	if _, resolveErr := engine.resolve(threeXUIBackupContainer); !errdefs.IsNotFound(resolveErr) {
		t.Fatalf("old rollback container survived a reconciled commit: %v", resolveErr)
	}
}

func TestReplaceThreeXUIContainerCreateFailureDoesNotStopCurrent(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, true)
	engine.failCreate = true
	if _, err := replaceThreeXUIContainer(context.Background(), engine, threeXUITestCreateOptions("deployment-1"), true, func(string) (string, error) { return "", nil }, acceptThreeXUIPromotion); err == nil {
		t.Fatal("candidate creation failure was ignored")
	}
	current, _ := engine.resolve(threeXUIContainer)
	if !current.running || len(engine.restored) != 0 {
		t.Fatalf("create failure changed the current service: %#v", current)
	}
}

func TestReplaceThreeXUIContainerRejectsUnownedCurrentWithoutMutation(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, true)
	engine.containers["old"].labels = nil
	if _, err := replaceThreeXUIContainer(context.Background(), engine, threeXUITestCreateOptions("deployment-2"), true, func(string) (string, error) {
		return "", nil
	}, acceptThreeXUIPromotion); err == nil {
		t.Fatal("unowned current container was accepted")
	}
	current, err := engine.resolve(threeXUIContainer)
	if err != nil || !current.running || engine.startCalls != 0 {
		t.Fatalf("unowned current container was mutated: %#v err=%v starts=%d", current, err, engine.startCalls)
	}
	if _, err := engine.resolve(threeXUICandidateContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("candidate was created before ownership validation: %v", err)
	}
}

func TestReplaceThreeXUIContainerRejectsAnotherApplicationWithoutMutation(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, true)
	engine.containers["old"].labels = applicationResourceLabels(threeXUIKey, "3x-ui", "application-2", "deployment-old")
	engine.volumeLabels = applicationResourceLabels(threeXUIKey, applicationVolumeComponent(threeXUIDatabaseVolume), "application-2", "")
	if _, err := replaceThreeXUIContainer(context.Background(), engine, threeXUITestCreateOptions("deployment-new"), true, func(string) (string, error) {
		return "", nil
	}, acceptThreeXUIPromotion); err == nil {
		t.Fatal("another application's 3x-ui resources were accepted")
	}
	current, err := engine.resolve(threeXUIContainer)
	if err != nil || !current.running || engine.startCalls != 0 {
		t.Fatalf("another application's resources were mutated: %#v err=%v starts=%d", current, err, engine.startCalls)
	}
	if _, err := engine.resolve(threeXUICandidateContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("candidate was created before application validation: %v", err)
	}
}

func TestRecoverInterruptedThreeXUIDeployRejectsUnownedCandidateWithoutMutation(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("candidate-id", threeXUICandidateContainer, true)
	engine.containers["candidate-id"].labels = nil
	if err := recoverInterruptedThreeXUIDeploy(context.Background(), engine); err == nil {
		t.Fatal("unowned recovery marker was accepted")
	}
	candidate, err := engine.resolve(threeXUICandidateContainer)
	if err != nil || !candidate.running || engine.startCalls != 0 {
		t.Fatalf("unowned recovery marker was mutated: %#v err=%v starts=%d", candidate, err, engine.startCalls)
	}
}

func TestReplaceThreeXUIContainerRejectsUnownedDatabaseWithoutMutation(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, true)
	engine.volumeLabels = nil
	if _, err := replaceThreeXUIContainer(context.Background(), engine, threeXUITestCreateOptions("deployment-2"), true, func(string) (string, error) {
		return "", nil
	}, acceptThreeXUIPromotion); err == nil {
		t.Fatal("unowned database volume was accepted")
	}
	current, err := engine.resolve(threeXUIContainer)
	if err != nil || !current.running || engine.startCalls != 0 {
		t.Fatalf("current container changed before volume ownership validation: %#v err=%v starts=%d", current, err, engine.startCalls)
	}
	if _, err := engine.resolve(threeXUICandidateContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("candidate was created before volume ownership validation: %v", err)
	}
}

func TestReplaceThreeXUIContainerStartFailureRestoresCurrent(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, true)
	engine.failStartName = threeXUICandidateContainer
	if _, err := replaceThreeXUIContainer(context.Background(), engine, threeXUITestCreateOptions("deployment-1"), true, func(string) (string, error) { return "", nil }, acceptThreeXUIPromotion); err == nil || !strings.Contains(err.Error(), "start 3x-ui candidate") {
		t.Fatalf("candidate start error = %v", err)
	}
	current, resolveErr := engine.resolve(threeXUIContainer)
	if resolveErr != nil || current.id != "old" || !current.running || len(engine.restored) == 0 {
		t.Fatalf("start failure did not restore current container and database: %#v, err=%v", current, resolveErr)
	}
}

func TestRecoverInterruptedThreeXUIDeployRestoresDurableBackup(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("old", threeXUIBackupContainer, false)
	engine.add("candidate-id", threeXUICandidateContainer, true)
	engine.persisted = []byte("durable-snapshot-marker")
	if err := recoverInterruptedThreeXUIDeploy(context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	current, resolveErr := engine.resolve(threeXUIContainer)
	if resolveErr != nil || current.id != "old" || !current.running {
		t.Fatalf("durable backup was not restored: %#v, err=%v", current, resolveErr)
	}
	if _, err := engine.resolve(threeXUICandidateContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("interrupted candidate was retained: %v", err)
	}
	if len(engine.restored) == 0 {
		t.Fatal("durable database snapshot was not restored")
	}
}

func TestReplaceThreeXUIContainerRestoresRetainedVolumeWithoutCurrentContainer(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.volumeExists = true
	_, err := replaceThreeXUIContainer(context.Background(), engine, threeXUITestCreateOptions("deployment-1"), true, func(string) (string, error) {
		return "", errors.New("configuration failed")
	}, acceptThreeXUIPromotion)
	if err == nil || !strings.Contains(err.Error(), "configuration failed") {
		t.Fatalf("replace error = %v", err)
	}
	if len(engine.persisted) == 0 || len(engine.restored) == 0 {
		t.Fatal("retained database volume was not snapshotted and restored")
	}
	if _, err := engine.resolve(threeXUICandidateContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("failed retained-data candidate was not removed: %v", err)
	}
}

func TestRecoverInterruptedRetainedVolumeCandidateBeforeRemoval(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.volumeExists = true
	engine.add("candidate-id", threeXUICandidateContainer, true)
	engine.persisted = []byte("durable-snapshot-marker")
	if err := recoverInterruptedThreeXUIDeploy(context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	if len(engine.restored) == 0 {
		t.Fatal("interrupted retained database volume was not restored")
	}
	if _, err := engine.resolve(threeXUICandidateContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("restored retained-data candidate was not removed: %v", err)
	}
}

func TestReplaceThreeXUIContainerDoesNotHideCommittedBackupCleanupFailure(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, true)
	engine.failRemoveName = threeXUICleanupContainer
	token, err := replaceThreeXUIContainer(context.Background(), engine, threeXUITestCreateOptions("deployment-1"), true, func(string) (string, error) {
		return "new-token", nil
	}, acceptThreeXUIPromotion)
	if err != nil || token != "new-token" {
		t.Fatalf("committed deployment was reported as failed: token=%q err=%v", token, err)
	}
	current, resolveErr := engine.resolve(threeXUIContainer)
	if resolveErr != nil || current.id != "candidate-id" || !current.running {
		t.Fatalf("healthy promoted container was lost after cleanup failure: %#v, err=%v", current, resolveErr)
	}
	if _, resolveErr := engine.resolve(threeXUICleanupContainer); resolveErr != nil {
		t.Fatalf("failed backup cleanup lost its retry marker: %v", resolveErr)
	}
}

func TestThreeXUIMaintenanceCleansCommitAndRestoresInterruptedRollback(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("current-id", threeXUIContainer, false)
	engine.add("backup-id", threeXUICleanupContainer, false)
	if err := maintainThreeXUIContainers(context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.resolve(threeXUICleanupContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("committed rollback survived maintenance: %v", err)
	}
	current, err := engine.resolve(threeXUIContainer)
	if err != nil || current.running {
		t.Fatalf("maintenance changed current service state: %#v err=%v", current, err)
	}

	rollbackOnly := newFakeThreeXUIContainerEngine(t, false)
	rollbackOnly.add("backup-id", threeXUIBackupContainer, false)
	rollbackOnly.persisted = []byte("durable-snapshot-marker")
	if err := maintainThreeXUIContainers(context.Background(), rollbackOnly); err != nil {
		t.Fatal(err)
	}
	restored, err := rollbackOnly.resolve(threeXUIContainer)
	if err != nil || !restored.running || len(rollbackOnly.restored) == 0 {
		t.Fatalf("interrupted rollback was not restored by maintenance: %#v err=%v", restored, err)
	}
}

func TestThreeXUIKeepDataUninstallRemovesAllTransactionalContainers(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, true)
	engine.add("candidate-id", threeXUICandidateContainer, false)
	engine.add("rollback-id", threeXUIBackupContainer, false)
	if err := uninstallDockerApp(context.Background(), engine, threeXUIKey, threeXUITestApplicationID, false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{threeXUICandidateContainer, threeXUIBackupContainer, threeXUIContainer} {
		if _, err := engine.resolve(name); !errdefs.IsNotFound(err) {
			t.Fatalf("transactional container %q survived keep-data uninstall: %v", name, err)
		}
	}
	if !engine.volumeExists {
		t.Fatal("keep-data uninstall removed the retained database volume")
	}
}

func TestThreeXUIKeepDataUninstallNeverStartsStoppedCurrent(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("current-id", threeXUIContainer, false)
	engine.volumeExists = true
	if err := uninstallDockerApp(context.Background(), engine, threeXUIKey, threeXUITestApplicationID, false); err != nil {
		t.Fatal(err)
	}
	if engine.startCalls != 0 {
		t.Fatalf("keep-data uninstall started a stopped service %d times", engine.startCalls)
	}
	if !engine.volumeExists {
		t.Fatal("keep-data uninstall removed the retained database volume")
	}
}

func TestThreeXUIKeepDataUninstallRemovesRecoveryMarkersBeforeStoppingCurrent(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, true)
	engine.add("candidate-id", threeXUICandidateContainer, false)
	engine.add("rollback-id", threeXUIBackupContainer, false)

	// Model an Agent exit after the keep-data preparation commits but before
	// the outer uninstall removes the canonical container.
	if err := prepareThreeXUIKeepDataUninstall(context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	current, err := engine.resolve(threeXUIContainer)
	if err != nil || current.running {
		t.Fatalf("keep-data preparation did not leave only a stopped current: %#v err=%v", current, err)
	}
	for _, marker := range []string{threeXUICandidateContainer, threeXUIBackupContainer} {
		if _, err := engine.resolve(marker); !errdefs.IsNotFound(err) {
			t.Fatalf("recovery marker %q survived before the authoritative stop: %v", marker, err)
		}
	}
	if err := maintainThreeXUIContainers(context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	if engine.startCalls != 0 || current.running {
		t.Fatalf("maintenance reversed a committed keep-data stop: starts=%d current=%#v", engine.startCalls, current)
	}
}

func TestThreeXUIKeepDataUninstallDoesNotStopCurrentWhenMarkerCleanupFails(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, true)
	engine.add("candidate-id", threeXUICandidateContainer, false)
	engine.failRemoveName = threeXUICandidateContainer

	err := prepareThreeXUIKeepDataUninstall(context.Background(), engine)
	if err == nil || !strings.Contains(err.Error(), "remove stale 3x-ui candidate") {
		t.Fatalf("candidate cleanup error = %v", err)
	}
	current, resolveErr := engine.resolve(threeXUIContainer)
	if resolveErr != nil || !current.running {
		t.Fatalf("failed marker cleanup stopped the live service: %#v err=%v", current, resolveErr)
	}
}

func TestThreeXUIDeleteDataUninstallIgnoresBrokenRollbackState(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("candidate-id", threeXUICandidateContainer, true)
	engine.add("rollback-id", threeXUIBackupContainer, false)
	engine.volumeExists = true
	engine.failStartName = threeXUIBackupContainer
	if err := uninstallDockerApp(context.Background(), engine, threeXUIKey, threeXUITestApplicationID, true); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{threeXUICandidateContainer, threeXUIBackupContainer, threeXUIContainer} {
		if _, err := engine.resolve(name); !errdefs.IsNotFound(err) {
			t.Fatalf("transactional container %q survived delete-data uninstall: %v", name, err)
		}
	}
	if engine.volumeExists {
		t.Fatal("delete-data uninstall retained the 3x-ui database volume")
	}
	if engine.startCalls != 0 {
		t.Fatalf("delete-data uninstall attempted rollback start %d times", engine.startCalls)
	}
}

func TestThreeXUIKeepDataUninstallRecoversRollbackBeforeRemovingMarkers(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("candidate-id", threeXUICandidateContainer, true)
	engine.add("rollback-id", threeXUIBackupContainer, false)
	engine.persisted = []byte("durable-snapshot-marker")
	engine.volumeExists = true
	if err := uninstallDockerApp(context.Background(), engine, threeXUIKey, threeXUITestApplicationID, false); err != nil {
		t.Fatal(err)
	}
	if len(engine.restored) == 0 {
		t.Fatal("keep-data uninstall discarded the only rollback snapshot")
	}
	for _, name := range []string{threeXUICandidateContainer, threeXUIBackupContainer, threeXUIContainer} {
		if _, err := engine.resolve(name); !errdefs.IsNotFound(err) {
			t.Fatalf("transactional container %q survived recovered uninstall: %v", name, err)
		}
	}
	if !engine.volumeExists {
		t.Fatal("keep-data uninstall removed the recovered database volume")
	}
}

func TestThreeXUIKeepDataUninstallNormalizesRollbackBeforeCommittedStop(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("candidate-id", threeXUICandidateContainer, true)
	engine.add("rollback-id", threeXUIBackupContainer, false)
	engine.persisted = []byte("durable-snapshot-marker")
	engine.volumeExists = true

	if err := prepareThreeXUIKeepDataUninstall(context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	current, err := engine.resolve(threeXUIContainer)
	if err != nil || current.running {
		t.Fatalf("rollback was not normalized to a stopped canonical container: %#v err=%v", current, err)
	}
	for _, marker := range []string{threeXUICandidateContainer, threeXUIBackupContainer} {
		if _, err := engine.resolve(marker); !errdefs.IsNotFound(err) {
			t.Fatalf("rollback marker %q survived committed stop: %v", marker, err)
		}
	}
	startsAfterPrepare := engine.startCalls
	if err := maintainThreeXUIContainers(context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	if engine.startCalls != startsAfterPrepare || current.running {
		t.Fatalf("maintenance reversed normalized uninstall stop: starts=%d before=%d current=%#v", engine.startCalls, startsAfterPrepare, current)
	}
}

func TestThreeXUIKeepDataUninstallRemovesRestoredCandidateBeforeReturning(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("candidate-id", threeXUICandidateContainer, true)
	engine.setVolumeState("candidate-id", "retained")
	engine.persisted = []byte("durable-snapshot-marker")
	engine.volumeExists = true

	if err := prepareThreeXUIKeepDataUninstall(context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.resolve(threeXUICandidateContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("restored candidate marker survived committed preparation: %v", err)
	}
	startsAfterPrepare := engine.startCalls
	if err := maintainThreeXUIContainers(context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	if engine.startCalls != startsAfterPrepare {
		t.Fatalf("maintenance started a service after candidate-only uninstall: starts=%d before=%d", engine.startCalls, startsAfterPrepare)
	}
	if !engine.volumeExists {
		t.Fatal("candidate-only uninstall removed retained data")
	}
}

func TestRecoverInterruptedFreshCandidateRemovesEmptyVolume(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("candidate-id", threeXUICandidateContainer, false)
	engine.setVolumeState("candidate-id", "fresh")
	engine.volumeExists = true
	if err := recoverInterruptedThreeXUIDeploy(context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.resolve(threeXUICandidateContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("fresh interrupted candidate was retained: %v", err)
	}
	if engine.volumeExists {
		t.Fatal("empty database volume from interrupted first install was retained")
	}
}

func TestRecoverInterruptedFreshCandidateRetainsMarkerUntilVolumeRemovalSucceeds(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("candidate-id", threeXUICandidateContainer, false)
	engine.setVolumeState("candidate-id", "fresh")
	engine.volumeExists = true
	engine.failVolumeRemove = true
	if err := recoverInterruptedThreeXUIDeploy(context.Background(), engine); err == nil || !strings.Contains(err.Error(), "remove interrupted fresh") {
		t.Fatalf("fresh volume removal error = %v", err)
	}
	if _, err := engine.resolve(threeXUICandidateContainer); err != nil {
		t.Fatalf("failed volume cleanup lost its retry marker: %v", err)
	}
	if !engine.volumeExists {
		t.Fatal("failed volume cleanup reported the volume as removed")
	}
	engine.failVolumeRemove = false
	if err := maintainThreeXUIContainers(context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.resolve(threeXUICandidateContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("successful maintenance retained the cleanup marker: %v", err)
	}
	if engine.volumeExists {
		t.Fatal("successful maintenance retained the interrupted fresh volume")
	}
}

func TestThreeXUIKeepDataUninstallPreservesStartedFreshCandidateVolume(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("candidate-id", threeXUICandidateContainer, true)
	engine.setVolumeState("candidate-id", "fresh")
	engine.volumeExists = true
	if err := uninstallDockerApp(context.Background(), engine, threeXUIKey, threeXUITestApplicationID, false); err != nil {
		t.Fatal(err)
	}
	if !engine.volumeExists {
		t.Fatal("keep-data uninstall removed the started fresh candidate database")
	}
	if _, err := engine.resolve(threeXUICandidateContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("fresh candidate survived keep-data uninstall: %v", err)
	}
}

func TestThreeXUIKeepDataUninstallRenamesSnapshotlessCandidateBeforeStopping(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("candidate-id", threeXUICandidateContainer, true)
	engine.setVolumeState("candidate-id", "fresh")
	engine.volumeExists = true
	engine.failRenameName = threeXUIContainer

	err := prepareThreeXUIKeepDataUninstall(context.Background(), engine)
	if err == nil || !strings.Contains(err.Error(), "retain 3x-ui candidate") {
		t.Fatalf("candidate rename error = %v", err)
	}
	candidate, resolveErr := engine.resolve(threeXUICandidateContainer)
	if resolveErr != nil || !candidate.running {
		t.Fatalf("failed canonical rename stopped the only retained candidate: %#v err=%v", candidate, resolveErr)
	}
	if !engine.volumeExists {
		t.Fatal("failed canonical rename removed the only retained database")
	}
}

func TestRecoverInterruptedRetainedCandidateKeepsVolumeWithoutSnapshot(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("candidate-id", threeXUICandidateContainer, false)
	engine.setVolumeState("candidate-id", "retained")
	engine.volumeExists = true
	if err := recoverInterruptedThreeXUIDeploy(context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.resolve(threeXUICandidateContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("retained interrupted candidate was retained: %v", err)
	}
	if !engine.volumeExists {
		t.Fatal("retained database volume was removed without a durable snapshot")
	}
}

func TestReplaceThreeXUIContainerNeverRestoresWhileCandidateMayBeRunning(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, true)
	engine.failStopName = threeXUICandidateContainer
	_, err := replaceThreeXUIContainer(context.Background(), engine, threeXUITestCreateOptions("deployment-1"), true, func(string) (string, error) {
		return "", errors.New("configuration failed")
	}, acceptThreeXUIPromotion)
	if err == nil || !strings.Contains(err.Error(), "stop failed 3x-ui candidate") {
		t.Fatalf("rollback stop error = %v", err)
	}
	if len(engine.restored) != 0 {
		t.Fatal("rollback overwrote the shared database while the candidate was still running")
	}
	if _, err := engine.resolve(threeXUICandidateContainer); err != nil {
		t.Fatalf("candidate marker was removed after an uncertain stop: %v", err)
	}
	if _, err := engine.resolve(threeXUIBackupContainer); err != nil {
		t.Fatalf("rollback marker was removed after an uncertain stop: %v", err)
	}
}

func TestRecoverInterruptedThreeXUIDeployRestartsStoppedCurrentBeforeSnapshot(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("old", threeXUIContainer, false)
	engine.add("candidate-id", threeXUICandidateContainer, false)
	if err := recoverInterruptedThreeXUIDeploy(context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	current, resolveErr := engine.resolve(threeXUIContainer)
	if resolveErr != nil || current.id != "old" || !current.running {
		t.Fatalf("pre-snapshot crash did not restart the previous container: %#v, err=%v", current, resolveErr)
	}
	if _, err := engine.resolve(threeXUICandidateContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("interrupted candidate was retained: %v", err)
	}
}

func TestRecoverInterruptedThreeXUIDeployNeverRollsBackOrRestartsPromotedCurrent(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("promoted-id", threeXUIContainer, false)
	engine.add("old-id", threeXUICleanupContainer, false)
	engine.persisted = []byte("old-durable-snapshot")
	if err := recoverInterruptedThreeXUIDeploy(context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	current, resolveErr := engine.resolve(threeXUIContainer)
	if resolveErr != nil || current.id != "promoted-id" || current.running {
		t.Fatalf("committed promoted container state was not retained: %#v, err=%v", current, resolveErr)
	}
	if _, err := engine.resolve(threeXUICleanupContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("committed rollback container was retained: %v", err)
	}
	if len(engine.restored) != 0 {
		t.Fatal("old database snapshot overwrote a committed deployment")
	}
}

func TestRecoverInterruptedThreeXUIDeployFinishesLegacyLostRenameRollback(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("candidate-id", threeXUIContainer, false)
	engine.containers["candidate-id"].labels = threeXUITestLabels("new-deployment")
	engine.add("old-id", threeXUIBackupContainer, true)
	engine.persisted = []byte("old-durable-snapshot")
	if err := maintainThreeXUIContainers(context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	current, resolveErr := engine.resolve(threeXUIContainer)
	if resolveErr != nil || current.id != "old-id" || !current.running {
		t.Fatalf("legacy mixed rollback was not completed: %#v err=%v", current, resolveErr)
	}
	if len(engine.restored) == 0 {
		t.Fatal("legacy mixed rollback did not restore its durable database snapshot")
	}
	if _, err := engine.resolve("candidate-id"); !errdefs.IsNotFound(err) {
		t.Fatalf("legacy promoted candidate survived rollback: %v", err)
	}
}

func TestRecoverInterruptedLegacyRollbackRemainsRecoverableAfterBackupStopFailure(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("candidate-id", threeXUIContainer, false)
	engine.add("old-id", threeXUIBackupContainer, true)
	engine.persisted = []byte("old-durable-snapshot")
	engine.failStopName = threeXUIBackupContainer
	if err := maintainThreeXUIContainers(context.Background(), engine); err == nil || !strings.Contains(err.Error(), "stop 3x-ui rollback") {
		t.Fatalf("rollback stop error = %v", err)
	}
	if _, err := engine.resolve(threeXUIContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("ambiguous current name survived entry into durable rollback: %v", err)
	}
	if _, err := engine.resolve(threeXUIBackupContainer); err != nil {
		t.Fatalf("durable rollback marker was lost after stop failure: %v", err)
	}
	engine.failStopName = ""
	if err := maintainThreeXUIContainers(context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	current, err := engine.resolve(threeXUIContainer)
	if err != nil || current.id != "old-id" || !current.running || len(engine.restored) == 0 {
		t.Fatalf("durable rollback did not recover on retry: %#v err=%v", current, err)
	}
}

func TestRecoverInterruptedRollbackRetainsMarkerUntilRestartSucceeds(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("candidate-id", threeXUIContainer, false)
	engine.add("old-id", threeXUIBackupContainer, false)
	engine.persisted = []byte("old-durable-snapshot")
	engine.failStartName = threeXUIBackupContainer
	if err := maintainThreeXUIContainers(context.Background(), engine); err == nil || !strings.Contains(err.Error(), "restart restored") {
		t.Fatalf("rollback restart error = %v", err)
	}
	if _, err := engine.resolve(threeXUIBackupContainer); err != nil {
		t.Fatalf("failed restart lost its durable rollback marker: %v", err)
	}
	if _, err := engine.resolve(threeXUIContainer); !errdefs.IsNotFound(err) {
		t.Fatalf("failed restart exposed a stopped canonical container: %v", err)
	}
	engine.failStartName = ""
	if err := maintainThreeXUIContainers(context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	current, err := engine.resolve(threeXUIContainer)
	if err != nil || current.id != "old-id" || !current.running {
		t.Fatalf("rollback did not recover after restart became available: %#v err=%v", current, err)
	}
}

func TestRecoverInterruptedRollbackAcceptsLostFinalRenameResponse(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.add("candidate-id", threeXUIContainer, false)
	engine.add("old-id", threeXUIBackupContainer, false)
	engine.persisted = []byte("old-durable-snapshot")
	engine.failRenameAfterName = threeXUIContainer
	if err := maintainThreeXUIContainers(context.Background(), engine); err != nil {
		t.Fatal(err)
	}
	current, err := engine.resolve(threeXUIContainer)
	if err != nil || current.id != "old-id" || !current.running {
		t.Fatalf("lost rename response was not reconciled: %#v err=%v", current, err)
	}
}

func TestReplaceThreeXUIContainerRecreatesImplicitEmptyVolume(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.volumeExists = true
	engine.volumeLabels = applicationResourceLabels(threeXUIKey, applicationVolumeComponent(threeXUIDatabaseVolume), threeXUITestApplicationID, "")
	engine.snapshot = threeXUITestEmptyDatabaseArchive(t)
	token, err := replaceThreeXUIContainer(context.Background(), engine, threeXUITestCreateOptions("deployment-1"), true, func(string) (string, error) {
		return "token", nil
	}, acceptThreeXUIPromotion)
	if err != nil || token != "token" {
		t.Fatalf("replace result token=%q err=%v", token, err)
	}
	current, resolveErr := engine.resolve(threeXUIContainer)
	if resolveErr != nil || current.labels[threeXUIVolumeStateLabel] != "fresh" {
		t.Fatalf("empty implicit volume was not recreated as fresh: %#v, err=%v", current, resolveErr)
	}
	if len(engine.removedVolumes) == 0 || engine.removedVolumes[0] != threeXUIDatabaseVolume {
		t.Fatalf("empty implicit database volume was not removed: %#v", engine.removedVolumes)
	}
}

func TestReplaceThreeXUIContainerNeverDeletesPartiallyPopulatedRetainedVolume(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, false)
	engine.volumeExists = true
	engine.volumeLabels = applicationResourceLabels(threeXUIKey, applicationVolumeComponent(threeXUIDatabaseVolume), threeXUITestApplicationID, "")
	engine.snapshot = threeXUITestDatabaseBackupOnlyArchive(t)
	_, err := replaceThreeXUIContainer(context.Background(), engine, threeXUITestCreateOptions("deployment-1"), true, func(string) (string, error) {
		return "token", nil
	}, acceptThreeXUIPromotion)
	if !errors.Is(err, errThreeXUIDatabaseMissing) {
		t.Fatalf("partially populated retained volume error = %v", err)
	}
	if !engine.volumeExists || len(engine.removedVolumes) != 0 {
		t.Fatalf("partially populated retained volume was removed: exists=%t removed=%#v", engine.volumeExists, engine.removedVolumes)
	}
}

func TestReplaceThreeXUIContainerRestartsCurrentAfterLostStopResponse(t *testing.T) {
	engine := newFakeThreeXUIContainerEngine(t, true)
	engine.failStopAfter = threeXUIContainer
	_, err := replaceThreeXUIContainer(context.Background(), engine, threeXUITestCreateOptions("deployment-1"), true, func(string) (string, error) {
		return "token", nil
	}, acceptThreeXUIPromotion)
	if err == nil || !strings.Contains(err.Error(), "stop response lost") {
		t.Fatalf("lost stop response error = %v", err)
	}
	current, resolveErr := engine.resolve(threeXUIContainer)
	if resolveErr != nil || !current.running {
		t.Fatalf("current service remained stopped after a lost Stop response: %#v err=%v", current, resolveErr)
	}
}

func TestNormalizeThreeXUIDatabaseSnapshotCanonicalizesAndRejectsUnsafePaths(t *testing.T) {
	normalized, err := normalizeThreeXUIDatabaseSnapshot(threeXUITestDatabaseArchive(t))
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(bytes.NewReader(normalized))
	foundDatabase := false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(header.Name, "/") || strings.Contains(header.Name, "..") || (header.Name != "x-ui" && !strings.HasPrefix(header.Name, "x-ui/")) {
			t.Fatalf("unsafe normalized archive entry %q", header.Name)
		}
		foundDatabase = foundDatabase || header.Name == "x-ui/x-ui.db"
	}
	if !foundDatabase {
		t.Fatal("normalized archive lost x-ui.db")
	}

	var malicious bytes.Buffer
	writer := tar.NewWriter(&malicious)
	_ = writer.WriteHeader(&tar.Header{Name: "../x-ui/x-ui.db", Mode: 0o600, Size: 1})
	_, _ = writer.Write([]byte("x"))
	_ = writer.Close()
	if _, err := normalizeThreeXUIDatabaseSnapshot(malicious.Bytes()); err == nil {
		t.Fatal("path-traversal archive was accepted")
	}
}

func threeXUITestDatabaseArchive(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	entries := []struct {
		name string
		body string
	}{
		{name: "x-ui", body: ""},
		{name: "x-ui/x-ui.db", body: "old-database"},
	}
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0o600, Size: int64(len(entry.body))}
		if entry.body == "" {
			header.Typeflag = tar.TypeDir
			header.Mode = 0o700
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if entry.body != "" {
			if _, err := writer.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func threeXUITestEmptyDatabaseArchive(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: "x-ui", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func threeXUITestDatabaseBackupOnlyArchive(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: "x-ui", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		t.Fatal(err)
	}
	backup := []byte("recoverable-backup")
	if err := writer.WriteHeader(&tar.Header{Name: "x-ui/x-ui.db.bak", Mode: 0o600, Size: int64(len(backup))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(backup); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
