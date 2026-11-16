package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
)

const (
	defaultCloudflaredContainer  = "vastora-cloudflared"
	tunnelOriginNetwork          = "vastora-tunnel-origin"
	tunnelOriginNetworkComponent = "tunnel-origin-network"
)

type TunnelIngress struct {
	Hostname string `json:"hostname"`
	Service  string `json:"service"`
	Path     string `json:"path,omitempty"`
}

type TunnelDesiredState struct {
	Revision int64           `json:"revision"`
	Status   string          `json:"status"`
	Image    string          `json:"image"`
	Token    string          `json:"token,omitempty"`
	Ingress  []TunnelIngress `json:"ingress"`
}

func (state TunnelDesiredState) Validate() error {
	if state.Revision < 1 || (state.Status != "running" && state.Status != "stopped") {
		return errors.New("agent: invalid tunnel desired state")
	}
	if state.Status == "running" && (strings.TrimSpace(state.Image) == "" || strings.TrimSpace(state.Token) == "") {
		return errors.New("agent: running tunnel requires an image and token")
	}
	return nil
}

type TunnelProvisioner interface {
	Apply(context.Context, TunnelDesiredState) error
}

type DockerTunnelProvisioner struct {
	Socket         string
	Container      string
	StateDirectory string
}

func (provisioner DockerTunnelProvisioner) Apply(ctx context.Context, state TunnelDesiredState) error {
	if err := state.Validate(); err != nil {
		return err
	}
	socket := provisioner.Socket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	name := provisioner.Container
	if name == "" {
		name = defaultCloudflaredContainer
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return fmt.Errorf("agent: connect Docker for tunnel: %w", err)
	}
	defer docker.Close()
	return provisioner.apply(ctx, state, docker, name)
}

type tunnelDockerEngine interface {
	packageDockerEngine
	dockerruntime.NetworkEngine
}

func (provisioner DockerTunnelProvisioner) apply(ctx context.Context, state TunnelDesiredState, docker tunnelDockerEngine, name string) error {
	if err := dockerruntime.EnsureNetwork(ctx, docker); err != nil {
		return err
	}
	manager := cpaProxyManager{Docker: docker, StateDirectory: provisioner.StateDirectory}
	receipts, err := manager.receipts()
	if err != nil {
		return err
	}
	usePrivateOrigin := len(receipts) != 0
	var address netip.Addr
	if usePrivateOrigin {
		if err := dockerruntime.EnsureBridgeNetwork(ctx, docker, tunnelOriginNetwork, tunnelOriginNetworkComponent); err != nil {
			return err
		}
		ownedNetwork, err := docker.NetworkInspect(ctx, tunnelOriginNetwork, client.NetworkInspectOptions{})
		if err != nil {
			return errors.New("agent: cannot inspect isolated Tunnel origin network")
		}
		for id := range ownedNetwork.Network.Containers {
			x, err := docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
			if err != nil || strings.TrimPrefix(x.Container.Name, "/") != name || x.Container.Config == nil || x.Container.Config.Labels[dockerruntime.ComponentLabel] != "cloudflare-tunnel" {
				return errors.New("agent: isolated Tunnel origin network has a foreign endpoint")
			}
		}
		if state.Status == "running" {
			address, err = tunnelStaticAddress(ownedNetwork.Network)
			if err != nil {
				return err
			}
		}
	}
	// Revoke persisted trust and restart CPA without forwarded-header trust
	// before ContainerRemove can release the previous connector's address.
	if err := manager.all(ctx, false); err != nil {
		return err
	}
	if current, err := docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{}); err == nil {
		if current.Container.Config == nil || current.Container.Config.Labels[dockerruntime.ManagedLabel] != "true" || current.Container.Config.Labels[dockerruntime.ComponentLabel] != "cloudflare-tunnel" {
			return errors.New("agent: refusing to replace an unowned Tunnel connector")
		}
	} else if !errdefs.IsNotFound(err) {
		return errors.New("agent: cannot verify old Tunnel connector ownership")
	}
	if state.Status == "stopped" {
		if _, err := docker.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("agent: remove cloudflared: %w", err)
		}
		return nil
	}
	pull, err := docker.ImagePull(ctx, state.Image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("agent: pull cloudflared image: %w", err)
	}
	_, _ = io.Copy(io.Discard, pull)
	_ = pull.Close()
	if _, err := docker.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("agent: replace cloudflared: %w", err)
	}
	// Keep all existing Docker-name origins reachable on the original network.
	endpoints := dockerruntime.NetworkingConfig(dockerruntime.CloudflaredAlias)
	if usePrivateOrigin {
		endpoints.EndpointsConfig[tunnelOriginNetwork] = &network.EndpointSettings{GwPriority: 1, IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: address}}
	}
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: state.Image,
			Cmd:   []string{"tunnel", "--no-autoupdate", "run", "--token", state.Token},
			Labels: map[string]string{
				"io.vastora.managed":   "true",
				"io.vastora.component": "cloudflare-tunnel",
			},
		},
		HostConfig: &container.HostConfig{
			NetworkMode:   container.NetworkMode(dockerruntime.NetworkName),
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")},
		},
		NetworkingConfig: endpoints,
		Name:             name,
	})
	if err != nil {
		return fmt.Errorf("agent: create cloudflared: %w", err)
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("agent: start cloudflared: %w", err)
	}
	if name != defaultCloudflaredContainer {
		return nil // A custom connector never grants CPA proxy authority.
	}
	return manager.all(ctx, true)
}

// Derive one stable address from Docker's actual, non-overlapping owned pool,
// never a hard-coded production subnet. No application joins this network, so
// stopping/restarting Docker cannot lease a trusted origin IP to another app.
func tunnelStaticAddress(n network.Inspect) (netip.Addr, error) {
	for _, pool := range n.IPAM.Config {
		if !pool.Subnet.IsValid() || !pool.Subnet.Addr().Is4() || !pool.Subnet.Addr().IsPrivate() || pool.Subnet.Bits() > 29 {
			continue
		}
		address := pool.Subnet.Masked().Addr().Next()
		for range 32 {
			reserved := address == pool.Gateway
			for _, a := range pool.AuxAddress {
				reserved = reserved || address == a
			}
			if !reserved && pool.Subnet.Contains(address) {
				return address, nil
			}
			address = address.Next()
		}
	}
	return netip.Addr{}, errors.New("agent: isolated Tunnel network has no verified private IPv4 pool")
}
