// Package dockerruntime contains the shared Docker runtime contract used by
// Center infrastructure and Agent-managed workloads.
package dockerruntime

import (
	"context"
	"fmt"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

type NetworkEngine interface {
	NetworkInspect(context.Context, string, client.NetworkInspectOptions) (client.NetworkInspectResult, error)
	NetworkCreate(context.Context, string, client.NetworkCreateOptions) (client.NetworkCreateResult, error)
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
}

const (
	NetworkName = "vastora-runtime"

	CenterAlias      = "vastora-center"
	HeadscaleAlias   = "vastora-center-headscale"
	CaddyAlias       = "vastora-gateway-caddy"
	HAProxyAlias     = "vastora-gateway-haproxy"
	ThreeXUIAlias    = "vastora-3x-ui"
	CloudflaredAlias = "vastora-cloudflared"
)

const (
	ManagedLabel   = "io.vastora.managed"
	ComponentLabel = "io.vastora.component"
	NetworkLabel   = "io.vastora.network"
)

// EnsureNetwork creates the single private bridge used by Vastora-managed
// containers. Containers publish only the host ports they explicitly own.
func EnsureNetwork(ctx context.Context, docker NetworkEngine) error {
	return EnsureBridgeNetwork(ctx, docker, NetworkName, "runtime-network")
}

// EnsureBridgeNetwork creates or validates a named private bridge. Existing
// endpoints must also belong to Vastora, so an unrelated container cannot be
// silently admitted through a pre-existing network with copied labels.
func EnsureBridgeNetwork(ctx context.Context, docker NetworkEngine, name, component string) error {
	inspected, err := docker.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if err == nil {
		return validateNetwork(ctx, docker, inspected, name, component)
	}
	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("docker runtime: inspect network %s: %w", name, err)
	}
	if _, err := docker.NetworkCreate(ctx, name, client.NetworkCreateOptions{
		Driver: "bridge",
		Labels: map[string]string{ManagedLabel: "true", ComponentLabel: component, NetworkLabel: name},
	}); err != nil {
		concurrent, inspectErr := docker.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
		if inspectErr == nil {
			if validateErr := validateNetwork(ctx, docker, concurrent, name, component); validateErr == nil {
				return nil
			} else if errdefs.IsAlreadyExists(err) {
				return validateErr
			}
		}
		if errdefs.IsAlreadyExists(err) {
			return fmt.Errorf("docker runtime: inspect concurrently created network %s: %w", name, inspectErr)
		}
		return fmt.Errorf("docker runtime: create network %s: %w", name, err)
	}
	return nil
}

func validateNetwork(ctx context.Context, docker NetworkEngine, inspected client.NetworkInspectResult, name, component string) error {
	if inspected.Network.Driver != "bridge" {
		return fmt.Errorf("docker runtime: network %s uses driver %q, want bridge", name, inspected.Network.Driver)
	}
	if inspected.Network.Labels[ManagedLabel] != "true" || inspected.Network.Labels[ComponentLabel] != component || inspected.Network.Labels[NetworkLabel] != name {
		return fmt.Errorf("docker runtime: refusing to use unowned network %s", name)
	}
	for containerID := range inspected.Network.Containers {
		attached, err := docker.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
		if err != nil {
			return fmt.Errorf("docker runtime: inspect container %s attached to network %s: %w", containerID, name, err)
		}
		if attached.Container.Config == nil || attached.Container.Config.Labels[ManagedLabel] != "true" || attached.Container.Config.Labels[ComponentLabel] == "" {
			return fmt.Errorf("docker runtime: refusing network %s with unowned attached container %s", name, containerID)
		}
	}
	return nil
}

func NetworkingConfig(aliases ...string) *network.NetworkingConfig {
	return &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
		NetworkName: {Aliases: aliases},
	}}
}
