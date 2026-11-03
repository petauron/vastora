package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
)

type headscaleBootDocker struct {
	current client.ContainerInspectResult
	updates int
}

func (d *headscaleBootDocker) ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	raw, _ := json.Marshal(d.current)
	var result client.ContainerInspectResult
	_ = json.Unmarshal(raw, &result)
	return result, nil
}
func (d *headscaleBootDocker) ContainerUpdate(_ context.Context, id string, options client.ContainerUpdateOptions) (client.ContainerUpdateResult, error) {
	if id != d.current.Container.ID {
		return client.ContainerUpdateResult{}, errors.New("wrong immutable identity")
	}
	d.updates++
	d.current.Container.HostConfig.RestartPolicy = *options.RestartPolicy
	return client.ContainerUpdateResult{}, errors.New("update response lost")
}

func TestHeadscaleBootPolicyRetainsResourcesAndRespectsStoppedInstances(t *testing.T) {
	d := &headscaleBootDocker{current: client.ContainerInspectResult{Container: container.InspectResponse{
		ID: "original-headscale", Config: &container.Config{Labels: map[string]string{dockerruntime.ManagedLabel: "true", dockerruntime.ComponentLabel: "center-headscale"}},
		State: &container.State{Running: true}, HostConfig: &container.HostConfig{NetworkMode: dockerruntime.NetworkName, RestartPolicy: container.RestartPolicy{Name: "unless-stopped"}, Resources: container.Resources{Memory: 128 << 20}},
	}}}
	if err := ensureHeadscaleBootPolicy(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if d.updates != 1 || d.current.Container.HostConfig.Resources.Memory != 128<<20 {
		t.Fatal("boot migration changed resources or repeated the update")
	}
	d.current.Container.State.Running = false
	d.current.Container.HostConfig.RestartPolicy.Name = "unless-stopped"
	d.updates = 0
	if err := ensureHeadscaleBootPolicy(context.Background(), d); err != nil || d.updates != 0 {
		t.Fatal("stopped system service was changed")
	}
	d.current.Container.State.Running = true
	d.current.Container.Config.Labels[dockerruntime.ManagedLabel] = "false"
	if err := ensureHeadscaleBootPolicy(context.Background(), d); err == nil || d.updates != 0 {
		t.Fatal("foreign system service was changed")
	}
	_, host := (DockerHeadscaleInstaller{}).headscaleContainerConfig()
	if host.RestartPolicy.Name != "always" {
		t.Fatal("new system service does not return on boot")
	}
}
