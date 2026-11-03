package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"sort"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

func retainedContainerNetworks(current client.ContainerInspectResult) []RuntimeNetworkAttachment {
	if current.Container.State == nil || !current.Container.State.Running || current.Container.NetworkSettings == nil {
		return nil
	}
	var result []RuntimeNetworkAttachment
	for name, endpoint := range current.Container.NetworkSettings.Networks {
		if endpoint == nil || endpoint.EndpointID == "" || endpoint.NetworkID == "" {
			return nil
		}
		aliases := slices.Clone(endpoint.Aliases)
		if name == "bridge" {
			aliases = nil
		}
		slices.Sort(aliases)
		aliases = slices.Compact(aliases)
		result = append(result, RuntimeNetworkAttachment{Name: name, ID: endpoint.NetworkID, Aliases: aliases, IPAM: endpoint.IPAMConfig})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// Capture ready installed resources at startup, never on heartbeat/reconnect.
// Existing non-empty ownership evidence is not replaced by a missing network.
func (s *Store) recoverPackageNetworks(ctx context.Context, docker gatewayAttachmentEngine) error {
	installed, err := s.RestorableInstallations(ctx)
	if err != nil {
		return err
	}
	executor := PackageExecutor{StateDirectory: s.dataDir}
	var failures []error
	for _, app := range installed {
		receipt, err := executor.ReadResources(app.ApplicationID)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if receipt.Runtime != "docker" || receipt.State != "ready" || receipt.AppKey != app.AppKey {
			continue
		}
		changed := false
		for i := range receipt.Resources {
			resource := &receipt.Resources[i]
			if resource.Kind != "container" || resource.ID == "" {
				continue
			}
			current, err := docker.ContainerInspect(ctx, resource.ID, client.ContainerInspectOptions{})
			if err != nil {
				failures = append(failures, errors.New("agent: retained application container is unavailable"))
				continue
			}
			v := current.Container
			if v.ID != resource.ID || v.Config == nil || v.HostConfig == nil || v.State == nil ||
				v.Config.Labels["io.vastora.managed"] != "true" ||
				v.Config.Labels[applicationIdentityLabel] != receipt.AppKey ||
				v.Config.Labels[applicationInstallationLabel] != receipt.ApplicationID ||
				v.Config.Labels["io.vastora.component"] != resource.Component {
				failures = append(failures, errors.New("agent: retained application container ownership changed"))
				continue
			}
			if v.HostConfig.NetworkMode == "host" || v.HostConfig.NetworkMode == "none" {
				continue
			}
			if v.State.Paused || v.State.Restarting || v.State.Dead {
				continue
			}
			if len(resource.Networks) != 0 && (v.State.Running || gatewayBindStartFailed(current)) {
				if err := recoverRetainedContainerNetworks(ctx, docker, current, resource.Networks); err != nil {
					failures = append(failures, fmt.Errorf("agent: restore retained application network: %w", err))
					continue
				}
				current, err = docker.ContainerInspect(ctx, resource.ID, client.ContainerInspectOptions{})
				if err != nil {
					failures = append(failures, err)
					continue
				}
			}
			observed := retainedContainerNetworks(current)
			if len(observed) != 0 && !reflect.DeepEqual(resource.Networks, observed) {
				if len(resource.Networks) != 0 {
					failures = append(failures, errors.New("agent: retained application network identity changed"))
					continue
				}
				resource.Networks, changed = observed, true
			}
		}
		if changed {
			if err := executor.save(receipt); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

func recoverRetainedContainerNetworks(ctx context.Context, docker gatewayAttachmentEngine, current client.ContainerInspectResult, expected []RuntimeNetworkAttachment) error {
	v := current.Container
	if v.State == nil || v.HostConfig == nil || v.ID == "" {
		return errors.New("retained container identity is incomplete")
	}
	allowed := make(map[string]RuntimeNetworkAttachment, len(expected))
	for _, saved := range expected {
		if saved.ID == "" || saved.Name == "" {
			return errors.New("retained network identity is incomplete")
		}
		n, err := docker.NetworkInspect(ctx, saved.ID, client.NetworkInspectOptions{})
		if err != nil || n.Network.ID != saved.ID || n.Network.Name != saved.Name || n.Network.Driver != "bridge" {
			return errors.New("retained network was replaced")
		}
		allowed[saved.Name] = saved
	}
	if _, ok := allowed[string(v.HostConfig.NetworkMode)]; !ok {
		return errors.New("retained primary network differs from the installed runtime")
	}
	if v.NetworkSettings != nil {
		for name := range v.NetworkSettings.Networks {
			if _, ok := allowed[name]; !ok {
				return errors.New("unexpected application network attachment")
			}
		}
	}
	if !v.State.Running {
		if !gatewayBindStartFailed(current) {
			return nil
		}
		if err := waitForPublishedAddresses(ctx, v.HostConfig.PortBindings); err != nil {
			return err
		}
		_, startErr := docker.ContainerStart(ctx, v.ID, client.ContainerStartOptions{})
		started, err := docker.ContainerInspect(ctx, v.ID, client.ContainerInspectOptions{})
		if err != nil || started.Container.State == nil || !started.Container.State.Running {
			return errors.Join(errors.New("retained application did not resume"), startErr, err)
		}
		current = started
	}
	for _, saved := range expected {
		if retainedEndpointPresent(current, saved) {
			continue
		}
		_, connectErr := docker.NetworkConnect(ctx, saved.ID, client.NetworkConnectOptions{Container: v.ID, EndpointConfig: &network.EndpointSettings{Aliases: saved.Aliases, IPAMConfig: saved.IPAM}})
		readback, err := docker.ContainerInspect(ctx, v.ID, client.ContainerInspectOptions{})
		if err != nil || !retainedEndpointPresent(readback, saved) {
			return errors.Join(errors.New("retained application network did not recover"), connectErr, err)
		}
		current = readback
	}
	return nil
}

func retainedEndpointPresent(current client.ContainerInspectResult, expected RuntimeNetworkAttachment) bool {
	if current.Container.NetworkSettings == nil || current.Container.HostConfig == nil {
		return false
	}
	endpoint := current.Container.NetworkSettings.Networks[expected.Name]
	if endpoint == nil || endpoint.NetworkID != expected.ID || endpoint.EndpointID == "" {
		return false
	}
	for _, alias := range expected.Aliases {
		if !slices.Contains(endpoint.Aliases, alias) {
			return false
		}
	}
	for port, bindings := range current.Container.HostConfig.PortBindings {
		if len(bindings) != 0 && !reflect.DeepEqual(bindings, current.Container.NetworkSettings.Ports[port]) {
			return false
		}
	}
	return reflect.DeepEqual(endpoint.IPAMConfig, expected.IPAM)
}
