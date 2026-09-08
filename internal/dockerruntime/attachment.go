package dockerruntime

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

type AttachmentEngine interface {
	NetworkEngine
	NetworkConnect(context.Context, string, client.NetworkConnectOptions) (client.NetworkConnectResult, error)
	NetworkDisconnect(context.Context, string, client.NetworkDisconnectOptions) (client.NetworkDisconnectResult, error)
}

// RecoverAttachment repairs only a proven managed bridge and container. Docker
// can retain HostConfig after boot while its live endpoint/port map is empty.
// Preserve the container and volumes; never turn a private bind into a wildcard.
func RecoverAttachment(ctx context.Context, docker AttachmentEngine, containerID, networkName, networkComponent, alias string) error {
	if err := EnsureBridgeNetwork(ctx, docker, networkName, networkComponent); err != nil {
		return err
	}
	current, err := docker.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	if current.Container.Config == nil || current.Container.HostConfig == nil || current.Container.Config.Labels[ManagedLabel] != "true" || current.Container.Config.Labels[ComponentLabel] == "" || string(current.Container.HostConfig.NetworkMode) != networkName {
		return errors.New("docker runtime: refusing network repair for an unowned or mismatched container")
	}
	if AttachmentHealthy(current, networkName, alias) {
		return nil
	}
	var endpoint *network.EndpointSettings
	aliases := []string{alias}
	restored := &network.EndpointSettings{}
	if current.Container.NetworkSettings != nil {
		endpoint = current.Container.NetworkSettings.Networks[networkName]
	}
	if endpoint != nil {
		aliases = slices.Clone(endpoint.Aliases)
		if !slices.Contains(aliases, alias) {
			aliases = append(aliases, alias)
		}
		if endpoint.IPAMConfig != nil {
			return errors.New("docker runtime: refusing to replace a custom static network attachment")
		}
		// Preserve configured endpoint behavior, not stale runtime addresses or
		// IDs. In particular, losing GwPriority can change the container's exit.
		restored.Links = slices.Clone(endpoint.Links)
		restored.DriverOpts = maps.Clone(endpoint.DriverOpts)
		restored.GwPriority = endpoint.GwPriority
		if _, err := docker.NetworkDisconnect(ctx, networkName, client.NetworkDisconnectOptions{Container: containerID}); err != nil {
			return fmt.Errorf("docker runtime: disconnect incomplete managed endpoint: %w", err)
		}
	}
	restored.Aliases = aliases
	confirmed := func(value client.ContainerInspectResult) bool {
		if !AttachmentHealthy(value, networkName, alias) {
			return false
		}
		actual := value.Container.NetworkSettings.Networks[networkName]
		for _, name := range aliases {
			if !slices.Contains(actual.Aliases, name) {
				return false
			}
		}
		return slices.Equal(actual.Links, restored.Links) && maps.Equal(actual.DriverOpts, restored.DriverOpts) && actual.GwPriority == restored.GwPriority
	}
	if _, err := docker.NetworkConnect(ctx, networkName, client.NetworkConnectOptions{Container: containerID, EndpointConfig: restored}); err != nil {
		// A lost response is not proof of failure; read back the immutable ID.
		verified, inspectErr := docker.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
		if inspectErr != nil || !confirmed(verified) {
			return errors.Join(fmt.Errorf("docker runtime: restore managed endpoint: %w", err), inspectErr)
		}
	}
	verified, err := docker.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	if !confirmed(verified) {
		return errors.New("docker runtime: managed network attachment or published ports did not recover")
	}
	return nil
}

func AttachmentHealthy(current client.ContainerInspectResult, networkName, alias string) bool {
	if current.Container.HostConfig == nil || current.Container.NetworkSettings == nil {
		return false
	}
	endpoint := current.Container.NetworkSettings.Networks[networkName]
	if endpoint == nil || endpoint.EndpointID == "" || !slices.Contains(endpoint.Aliases, alias) {
		return false
	}
	for port, expected := range current.Container.HostConfig.PortBindings {
		if len(expected) != 0 && !reflect.DeepEqual(expected, current.Container.NetworkSettings.Ports[port]) {
			return false
		}
	}
	return true
}
