package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
)

const (
	defaultCloudflaredContainer = "vastora-cloudflared"
)

type TunnelIngress struct {
	Hostname string `json:"hostname"`
	Service  string `json:"service"`
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
	Socket    string
	Container string
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
	if err := dockerruntime.EnsureNetwork(ctx, docker); err != nil {
		return err
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
		NetworkingConfig: dockerruntime.NetworkingConfig(dockerruntime.CloudflaredAlias),
		Name:             name,
	})
	if err != nil {
		return fmt.Errorf("agent: create cloudflared: %w", err)
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("agent: start cloudflared: %w", err)
	}
	return nil
}
