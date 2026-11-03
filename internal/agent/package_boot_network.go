package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"

	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// Runtime network observations are local recovery evidence. Keep them outside
// resources.json: Center authorizes handovers using that receipt's exact digest.
type RuntimeNetworkAttachment struct {
	Name    string                      `json:"name"`
	ID      string                      `json:"id"`
	Aliases []string                    `json:"aliases,omitempty"`
	IPAM    *network.EndpointIPAMConfig `json:"ipam,omitempty"`
}

type packageBootNetworks struct {
	ApplicationID string                                `json:"applicationId"`
	AppKey        string                                `json:"appKey"`
	Containers    map[string][]RuntimeNetworkAttachment `json:"containers"`
}

func (e PackageExecutor) bootNetworkPath(id string) string {
	return filepath.Join(filepath.Dir(e.receiptPath(id)), "boot-networks.json")
}

func (e PackageExecutor) readBootNetworks(receipt *InstanceResources) (packageBootNetworks, error) {
	empty := packageBootNetworks{ApplicationID: receipt.ApplicationID, AppKey: receipt.AppKey, Containers: map[string][]RuntimeNetworkAttachment{}}
	path := e.bootNetworkPath(receipt.ApplicationID)
	if err := checkPackageParents(path); err != nil {
		return empty, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return empty, errors.New("agent: unsafe package boot network evidence")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return empty, err
	}
	var saved packageBootNetworks
	if json.Unmarshal(raw, &saved) != nil || saved.ApplicationID != receipt.ApplicationID || saved.Containers == nil {
		return empty, errors.New("agent: invalid package boot network evidence")
	}
	// An authorized package identity replacement retires the previous observations.
	if saved.AppKey != receipt.AppKey {
		return empty, nil
	}
	return saved, nil
}

func (e PackageExecutor) captureBootNetworks(receipt *InstanceResources, current client.ContainerInspectResult) error {
	observed := retainedContainerNetworks(current)
	if len(observed) == 0 {
		return nil
	}
	found := false
	for _, resource := range receipt.Resources {
		if resource.Kind == "container" && resource.ID == current.Container.ID {
			found = true
		}
	}
	if !found {
		return errors.New("agent: boot network container is outside the package receipt")
	}
	saved, err := e.readBootNetworks(receipt)
	if err != nil {
		return err
	}
	if reflect.DeepEqual(saved.Containers[current.Container.ID], observed) {
		return nil
	}
	retained := map[string][]RuntimeNetworkAttachment{}
	for _, resource := range receipt.Resources {
		if resource.Kind == "container" && len(saved.Containers[resource.ID]) != 0 {
			retained[resource.ID] = saved.Containers[resource.ID]
		}
	}
	retained[current.Container.ID] = observed
	saved.Containers = retained
	raw, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	return writeHostFileAtomic(e.bootNetworkPath(receipt.ApplicationID), raw, 0600)
}

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
		saved, err := executor.readBootNetworks(receipt)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, resource := range receipt.Resources {
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
			expected := saved.Containers[resource.ID]
			if len(expected) != 0 && (v.State.Running || gatewayBindStartFailed(current)) {
				if err := recoverRetainedContainerNetworks(ctx, docker, current, expected); err != nil {
					failures = append(failures, fmt.Errorf("agent: restore retained application network: %w", err))
					continue
				}
				current, err = docker.ContainerInspect(ctx, resource.ID, client.ContainerInspectOptions{})
				if err != nil {
					failures = append(failures, err)
					continue
				}
			}
			if err := executor.captureBootNetworks(receipt, current); err != nil {
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

	// Docker owns and recreates the built-in bridge on daemon boot. Its name,
	// driver, scope and reserved default-bridge flag are the stable identity;
	// custom networks must still match the recorded immutable ID.
	expected = slices.Clone(expected)
	allowed := make(map[string]RuntimeNetworkAttachment, len(expected))
	for i, saved := range expected {
		if saved.ID == "" || saved.Name == "" {
			return errors.New("retained network identity is incomplete")
		}
		if saved.Name == "bridge" {
			currentBridge, err := builtinBridgeAttachment(ctx, docker)
			if err != nil {
				return err
			}
			saved.ID = currentBridge.ID
			expected[i] = saved
		} else {
			n, err := docker.NetworkInspect(ctx, saved.ID, client.NetworkInspectOptions{})
			if err != nil || n.Network.ID != saved.ID || n.Network.Name != saved.Name || n.Network.Driver != "bridge" {
				return errors.New("retained network was replaced")
			}
		}
		allowed[saved.Name] = saved
	}
	primary := string(v.HostConfig.NetworkMode)
	if primary == "bridge" || primary == "default" {
		// Historical apps can join an additional shared network while preserving
		// Docker's declared default primary mode. Starting the original container
		// restores that implicit bridge; it is not an unexpected custom attachment.
		if _, ok := allowed["bridge"]; !ok {
			bridge, err := builtinBridgeAttachment(ctx, docker)
			if err != nil {
				return err
			}
			expected = append(expected, bridge)
			allowed["bridge"] = bridge
		}
	} else {
		found := false
		for _, saved := range expected {
			if primary == saved.Name || primary == saved.ID {
				found = true
			}
		}
		if !found {
			return errors.New("retained primary network differs from the installed runtime")
		}
	}

	if v.NetworkSettings != nil {
		for name := range v.NetworkSettings.Networks {
			if _, ok := allowed[name]; !ok {
				return errors.New("unexpected application network attachment")
			}
		}
	}
	if v.State.Running && v.NetworkSettings != nil && v.NetworkSettings.SandboxID == "" {
		for _, endpoint := range v.NetworkSettings.Networks {
			if endpoint != nil && endpoint.EndpointID != "" {
				return errors.New("retained application has an inconsistent active sandbox")
			}
		}
		// Docker may report Running after a failed boot bind while retaining
		// no sandbox at all. Connecting a network cannot repair that state.
		// Restart only this proven orphan, using its unchanged configuration.
		if err := waitForPublishedAddresses(ctx, v.HostConfig.PortBindings); err != nil {
			return err
		}
		timeout := 10
		_, restartErr := docker.ContainerRestart(ctx, v.ID, client.ContainerRestartOptions{Timeout: &timeout})
		recovered, err := docker.ContainerInspect(ctx, v.ID, client.ContainerInspectOptions{})
		if err != nil || recovered.Container.State == nil || !recovered.Container.State.Running || recovered.Container.NetworkSettings == nil || recovered.Container.NetworkSettings.SandboxID == "" {
			return errors.Join(errors.New("retained application sandbox did not recover"), restartErr, err)
		}
		current = recovered
		v = current.Container
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

func builtinBridgeAttachment(ctx context.Context, docker gatewayAttachmentEngine) (RuntimeNetworkAttachment, error) {
	n, err := docker.NetworkInspect(ctx, "bridge", client.NetworkInspectOptions{})
	if err != nil || n.Network.ID == "" || n.Network.Name != "bridge" || n.Network.Driver != "bridge" || n.Network.Scope != "local" || n.Network.Options["com.docker.network.bridge.default_bridge"] != "true" {
		return RuntimeNetworkAttachment{}, errors.New("docker built-in default bridge identity changed")
	}
	return RuntimeNetworkAttachment{Name: "bridge", ID: n.Network.ID}, nil
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
