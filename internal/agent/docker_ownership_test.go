package agent

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

type fakeApplicationResourceEngine struct {
	containers         map[string]client.ContainerInspectResult
	volumes            map[string]client.VolumeInspectResult
	containerRemoves   int
	volumeCreates      int
	volumeRemoves      int
	lostContainerReply bool
	lostVolumeReply    bool
	volumeCreateErr    error
}

func (engine *fakeApplicationResourceEngine) ContainerInspect(_ context.Context, value string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	for name, inspected := range engine.containers {
		if value == name || value == inspected.Container.ID {
			return inspected, nil
		}
	}
	return client.ContainerInspectResult{}, errdefs.ErrNotFound.WithMessage("container not found")
}

func (engine *fakeApplicationResourceEngine) ContainerRemove(_ context.Context, value string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	engine.containerRemoves++
	for name, inspected := range engine.containers {
		if value == name || value == inspected.Container.ID {
			delete(engine.containers, name)
			if engine.lostContainerReply {
				return client.ContainerRemoveResult{}, errors.New("remove response lost")
			}
			return client.ContainerRemoveResult{}, nil
		}
	}
	return client.ContainerRemoveResult{}, errdefs.ErrNotFound.WithMessage("container not found")
}

func (engine *fakeApplicationResourceEngine) VolumeInspect(_ context.Context, name string, _ client.VolumeInspectOptions) (client.VolumeInspectResult, error) {
	inspected, ok := engine.volumes[name]
	if !ok {
		return client.VolumeInspectResult{}, errdefs.ErrNotFound.WithMessage("volume not found")
	}
	return inspected, nil
}

func (engine *fakeApplicationResourceEngine) VolumeCreate(_ context.Context, options client.VolumeCreateOptions) (client.VolumeCreateResult, error) {
	engine.volumeCreates++
	created := volume.Volume{Name: options.Name, Labels: maps.Clone(options.Labels)}
	engine.volumes[options.Name] = client.VolumeInspectResult{Volume: created}
	return client.VolumeCreateResult{Volume: created}, engine.volumeCreateErr
}

func (engine *fakeApplicationResourceEngine) VolumeRemove(_ context.Context, name string, _ client.VolumeRemoveOptions) (client.VolumeRemoveResult, error) {
	engine.volumeRemoves++
	delete(engine.volumes, name)
	if engine.lostVolumeReply {
		return client.VolumeRemoveResult{}, errors.New("remove response lost")
	}
	return client.VolumeRemoveResult{}, nil
}

func ownedTestContainer(name, applicationID, deploymentID string) client.ContainerInspectResult {
	return client.ContainerInspectResult{Container: container.InspectResponse{
		ID: name + "-id", Config: &container.Config{Labels: applicationResourceLabels("app", "component", applicationID, deploymentID)},
	}}
}

func ownedTestVolume(name, applicationID string) client.VolumeInspectResult {
	return client.VolumeInspectResult{Volume: volume.Volume{
		Name: name, Labels: applicationResourceLabels("app", applicationVolumeComponent(name), applicationID, ""),
	}}
}

func TestRemoveOwnedApplicationContainerRejectsUnownedNameCollision(t *testing.T) {
	engine := &fakeApplicationResourceEngine{
		containers: map[string]client.ContainerInspectResult{"shared": {Container: container.InspectResponse{ID: "foreign", Config: &container.Config{}}}},
	}
	if err := removeOwnedApplicationContainer(context.Background(), engine, "shared", "app", "component", "application-1", anyApplicationDeployment); err == nil {
		t.Fatal("unowned container was accepted")
	}
	if engine.containerRemoves != 0 {
		t.Fatal("unowned container was removed")
	}
}

func TestValidateApplicationResourceLabelsRejectsEveryIdentityMismatch(t *testing.T) {
	valid := applicationResourceLabels("app", "component", "application-1", "deployment-1")
	for name, mutate := range map[string]func(map[string]string){
		"managed":     func(labels map[string]string) { labels["io.vastora.managed"] = "false" },
		"component":   func(labels map[string]string) { labels["io.vastora.component"] = "other" },
		"application": func(labels map[string]string) { labels[applicationIdentityLabel] = "other" },
		"installation": func(labels map[string]string) {
			labels[applicationInstallationLabel] = "application-2"
		},
		"deployment": func(labels map[string]string) { labels[applicationDeploymentIDLabel] = "deployment-2" },
	} {
		t.Run(name, func(t *testing.T) {
			labels := maps.Clone(valid)
			mutate(labels)
			if err := validateApplicationResourceLabels(labels, "app", "component", "application-1", "deployment-1"); err == nil {
				t.Fatal("mismatched labels were accepted")
			}
		})
	}
}

func TestRemoveOwnedApplicationContainerReconcilesLostResponse(t *testing.T) {
	engine := &fakeApplicationResourceEngine{
		containers:         map[string]client.ContainerInspectResult{"shared": ownedTestContainer("shared", "application-1", "deployment-1")},
		lostContainerReply: true,
	}
	if err := removeOwnedApplicationContainer(context.Background(), engine, "shared", "app", "component", "application-1", anyApplicationDeployment); err != nil {
		t.Fatal(err)
	}
	if err := removeOwnedApplicationContainer(context.Background(), engine, "shared", "app", "component", "application-1", anyApplicationDeployment); err != nil {
		t.Fatal(err)
	}
	if engine.containerRemoves != 1 {
		t.Fatalf("container removes = %d, want 1", engine.containerRemoves)
	}
}

func TestEnsureOwnedApplicationVolumeRejectsUnownedNameCollision(t *testing.T) {
	engine := &fakeApplicationResourceEngine{
		volumes: map[string]client.VolumeInspectResult{"shared": {Volume: volume.Volume{Name: "shared"}}},
	}
	if err := ensureOwnedApplicationVolume(context.Background(), engine, "shared", "app", applicationVolumeComponent("shared"), "application-1"); err == nil {
		t.Fatal("unowned volume was accepted")
	}
	if engine.volumeCreates != 0 {
		t.Fatal("unowned volume was replaced")
	}
}

func TestEnsureOwnedApplicationVolumeIsReplaySafe(t *testing.T) {
	engine := &fakeApplicationResourceEngine{volumes: map[string]client.VolumeInspectResult{}}
	for range 2 {
		if err := ensureOwnedApplicationVolume(context.Background(), engine, "shared", "app", applicationVolumeComponent("shared"), "application-1"); err != nil {
			t.Fatal(err)
		}
	}
	if engine.volumeCreates != 1 {
		t.Fatalf("volume creates = %d, want 1", engine.volumeCreates)
	}
}

func TestEnsureOwnedApplicationVolumesPreflightsBeforeCreating(t *testing.T) {
	engine := &fakeApplicationResourceEngine{volumes: map[string]client.VolumeInspectResult{
		"foreign": {Volume: volume.Volume{Name: "foreign"}},
	}}
	if err := ensureOwnedApplicationVolumes(context.Background(), engine, []string{"missing", "foreign"}, "app", "application-1"); err == nil {
		t.Fatal("unowned volume was accepted")
	}
	if engine.volumeCreates != 0 {
		t.Fatalf("created %d volumes before preflight completed", engine.volumeCreates)
	}
}

func TestEnsureOwnedApplicationVolumeReconcilesLostCreateResponse(t *testing.T) {
	engine := &fakeApplicationResourceEngine{volumes: map[string]client.VolumeInspectResult{}, volumeCreateErr: errors.New("create response lost")}
	if err := ensureOwnedApplicationVolume(context.Background(), engine, "shared", "app", applicationVolumeComponent("shared"), "application-1"); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveOwnedApplicationVolumeReconcilesLostResponse(t *testing.T) {
	engine := &fakeApplicationResourceEngine{
		volumes:         map[string]client.VolumeInspectResult{"shared": ownedTestVolume("shared", "application-1")},
		lostVolumeReply: true,
	}
	if err := removeOwnedApplicationVolume(context.Background(), engine, "shared", "app", applicationVolumeComponent("shared"), "application-1"); err != nil {
		t.Fatal(err)
	}
	if err := removeOwnedApplicationVolume(context.Background(), engine, "shared", "app", applicationVolumeComponent("shared"), "application-1"); err != nil {
		t.Fatal(err)
	}
	if engine.volumeRemoves != 1 {
		t.Fatalf("volume removes = %d, want 1", engine.volumeRemoves)
	}
}
