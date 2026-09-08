package dockerruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

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
		// Disconnect removes endpoint configuration from Docker. A failed
		// reconnect (or process death) would leave the next retry unable to
		// recover external settings. Only detach our reproducible defaults;
		// never temporarily accept custom settings based on an in-memory copy.
		if !managedEndpointConfiguration(current, endpoint, alias) {
			return errors.New("docker runtime: refusing to detach a customized network attachment")
		}
		aliases = slices.Clone(endpoint.Aliases)
		if !slices.Contains(aliases, alias) {
			aliases = append(aliases, alias)
		}
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
		return managedEndpointConfiguration(value, actual, alias)
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

func managedEndpointConfiguration(current client.ContainerInspectResult, endpoint *network.EndpointSettings, alias string) bool {
	if endpoint.IPAMConfig != nil || len(endpoint.Links) != 0 || len(endpoint.DriverOpts) != 0 || endpoint.GwPriority != 0 {
		return false
	}
	known := []string{alias, strings.TrimPrefix(current.Container.Name, "/"), current.Container.ID}
	if len(current.Container.ID) >= 12 {
		known = append(known, current.Container.ID[:12])
	}
	for _, value := range endpoint.Aliases {
		if value == "" || !slices.Contains(known, value) {
			return false
		}
	}
	return true
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
