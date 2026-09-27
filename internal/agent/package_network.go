package agent

import (
	"context"
	"errors"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

type packageNetworkEngine interface {
	NetworkInspect(context.Context, string, client.NetworkInspectOptions) (client.NetworkInspectResult, error)
	NetworkCreate(context.Context, string, client.NetworkCreateOptions) (client.NetworkCreateResult, error)
	NetworkRemove(context.Context, string, client.NetworkRemoveOptions) (client.NetworkRemoveResult, error)
}

func (b *DockerPackageBackend) prepareNetwork(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	count := 0
	for _, plan := range b.plans {
		if plan.options.HostConfig.NetworkMode != "host" {
			count++
		}
	}
	if count < 2 {
		return nil
	}
	engine, ok := b.Docker.(packageNetworkEngine)
	if !ok {
		return errors.New("agent: Docker network capability unavailable")
	}
	name := runtimeName(receipt, "network", "private-network")
	current, err := engine.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if err == nil {
		old := resourceNamed(receipt, "network", "private-network")
		if old == nil || old.ID != current.Network.ID {
			return errors.New("agent: package network name collision")
		}
		if err := b.inspectNetwork(ctx, task, *old); err != nil {
			return err
		}
	} else if !errdefs.IsNotFound(err) {
		return errors.New("agent: package network inspection failed")
	}
	b.networkName = name
	for i := range b.plans {
		plan := &b.plans[i]
		if plan.options.HostConfig.NetworkMode == "host" {
			continue
		}
		plan.options.HostConfig.NetworkMode = container.NetworkMode(name)
		plan.options.NetworkingConfig = &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{name: {Aliases: []string{plan.logical}}}}
	}
	return nil
}

func (b *DockerPackageBackend) inspectNetwork(ctx context.Context, task DeploymentTask, resource RuntimeResource) error {
	engine, ok := b.Docker.(packageNetworkEngine)
	if !ok {
		return errors.New("agent: network ownership cannot be inspected")
	}
	current, err := engine.NetworkInspect(ctx, resource.Name, client.NetworkInspectOptions{})
	if err != nil || current.Network.ID != resource.ID || current.Network.Driver != "bridge" {
		return errors.New("agent: package network identity changed")
	}
	expected := applicationResourceLabels(task.AppKey, "package-network", task.ApplicationID, "")
	for key, value := range expected {
		if key != applicationDeploymentIDLabel && current.Network.Labels[key] != value {
			return errors.New("agent: package network ownership mismatch")
		}
	}
	for id := range current.Network.Containers {
		attached, err := b.Docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil || attached.Container.Config == nil || attached.Container.Config.Labels[applicationInstallationLabel] != task.ApplicationID || attached.Container.Config.Labels[applicationIdentityLabel] != task.AppKey {
			return errors.New("agent: foreign endpoint attached to package network")
		}
	}
	return nil
}

func (b *DockerPackageBackend) applyNetwork(ctx context.Context, task DeploymentTask, receipt *InstanceResources, persist func() error) error {
	if b.networkName == "" || resourceNamed(receipt, "network", "private-network") != nil {
		return nil
	}
	engine, ok := b.Docker.(packageNetworkEngine)
	if !ok {
		return errors.New("agent: Docker network capability unavailable")
	}
	created, err := engine.NetworkCreate(ctx, b.networkName, client.NetworkCreateOptions{Driver: "bridge", Labels: applicationResourceLabels(task.AppKey, "package-network", task.ApplicationID, task.ID)})
	if err != nil {
		return errors.New("agent: package private network could not be created")
	}
	receipt.Resources = append(receipt.Resources, RuntimeResource{Kind: "network", LogicalName: "private-network", Name: b.networkName, ID: created.ID, Component: "package-network"})
	return persist()
}
