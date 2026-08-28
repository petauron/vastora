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
)

// EnsureNetwork creates the single private bridge used by Vastora-managed
// containers. Containers publish only the host ports they explicitly own.
func EnsureNetwork(ctx context.Context, docker *client.Client) error {
	inspected, err := docker.NetworkInspect(ctx, NetworkName, client.NetworkInspectOptions{})
	if err == nil {
		if inspected.Network.Driver != "bridge" {
			return fmt.Errorf("docker runtime: network %s uses driver %q, want bridge", NetworkName, inspected.Network.Driver)
		}
		return nil
	}
	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("docker runtime: inspect network %s: %w", NetworkName, err)
	}
	if _, err := docker.NetworkCreate(ctx, NetworkName, client.NetworkCreateOptions{
		Driver: "bridge",
		Labels: map[string]string{ManagedLabel: "true", ComponentLabel: "runtime-network"},
	}); err != nil {
		if !errdefs.IsAlreadyExists(err) {
			return fmt.Errorf("docker runtime: create network %s: %w", NetworkName, err)
		}
		concurrent, inspectErr := docker.NetworkInspect(ctx, NetworkName, client.NetworkInspectOptions{})
		if inspectErr != nil {
			return fmt.Errorf("docker runtime: inspect concurrently created network %s: %w", NetworkName, inspectErr)
		}
		if concurrent.Network.Driver != "bridge" {
			return fmt.Errorf("docker runtime: network %s uses driver %q, want bridge", NetworkName, concurrent.Network.Driver)
		}
	}
	return nil
}

func NetworkingConfig(aliases ...string) *network.NetworkingConfig {
	return &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
		NetworkName: {Aliases: aliases},
	}}
}
