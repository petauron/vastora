package deployer

import (
	"context"
	"errors"
	"reflect"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
)

type headscaleBootEngine interface {
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerUpdate(context.Context, string, client.ContainerUpdateOptions) (client.ContainerUpdateResult, error)
}

// The registered control-plane service must return on Docker/host boot.
// Migrate only an already running owned instance; never start a stopped one.
func (installer DockerHeadscaleInstaller) EnsureHeadscaleBootPolicy(ctx context.Context) error {
	docker, err := client.New(client.WithHost(installer.Socket))
	if err != nil {
		return err
	}
	defer docker.Close()
	return ensureHeadscaleBootPolicy(ctx, docker)
}

func ensureHeadscaleBootPolicy(ctx context.Context, docker headscaleBootEngine) error {
	current, err := docker.ContainerInspect(ctx, DefaultHeadscaleContainer, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	v := current.Container
	if v.Config == nil || v.HostConfig == nil || v.State == nil || v.ID == "" ||
		v.Config.Labels[dockerruntime.ManagedLabel] != "true" || v.Config.Labels[dockerruntime.ComponentLabel] != "center-headscale" ||
		string(v.HostConfig.NetworkMode) != dockerruntime.NetworkName {
		return errors.New("deployer: system Headscale ownership changed")
	}
	if !v.State.Running || v.State.Paused || v.State.Restarting || v.State.Dead || v.HostConfig.RestartPolicy.Name == "always" {
		return nil
	}
	if v.HostConfig.RestartPolicy.Name != "unless-stopped" {
		return errors.New("deployer: system Headscale has a custom restart policy")
	}
	_, updateErr := docker.ContainerUpdate(ctx, v.ID, client.ContainerUpdateOptions{Resources: &v.HostConfig.Resources, RestartPolicy: &container.RestartPolicy{Name: "always"}})
	after, inspectErr := docker.ContainerInspect(ctx, v.ID, client.ContainerInspectOptions{})
	if inspectErr != nil || after.Container.ID != v.ID || after.Container.HostConfig == nil || after.Container.HostConfig.RestartPolicy.Name != "always" ||
		!reflect.DeepEqual(v.HostConfig.Resources, after.Container.HostConfig.Resources) || !reflect.DeepEqual(v.HostConfig.PortBindings, after.Container.HostConfig.PortBindings) {
		return errors.Join(errors.New("deployer: system Headscale boot policy was not confirmed"), updateErr, inspectErr)
	}
	return nil
}
