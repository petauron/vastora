package deployer

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/dockerruntime"
)

const (
	centerRemoteAccessContainer = "vastora-center-cloudflared"
	centerRemoteAccessComponent = "center-cloudflare-tunnel"
	centerRemoteAccessAlias     = "vastora-center-cloudflared"
)

type DockerCenterRemoteAccessManager struct {
	Socket string
}

func (manager DockerCenterRemoteAccessManager) ApplyCenterRemoteAccess(ctx context.Context, input deployapi.CenterRemoteAccessRequest) error {
	if err := deployapi.ValidateCenterRemoteAccessRequest(input); err != nil {
		return err
	}
	socket := strings.TrimSpace(manager.Socket)
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return fmt.Errorf("deployer: connect Docker for Center remote access: %w", err)
	}
	defer docker.Close()
	if err := dockerruntime.EnsureNetwork(ctx, docker); err != nil {
		return err
	}
	if !input.Enabled {
		return removeCenterRemoteAccessContainer(ctx, docker)
	}
	pull, err := docker.ImagePull(ctx, deployapi.CloudflaredImage, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("deployer: pull fixed cloudflared image: %w", err)
	}
	_, _ = io.Copy(io.Discard, pull)
	_ = pull.Close()
	if err := removeCenterRemoteAccessContainer(ctx, docker); err != nil {
		return err
	}
	created, err := docker.ContainerCreate(ctx, centerRemoteAccessCreateOptions(input.Token))
	if err != nil {
		return fmt.Errorf("deployer: create Center cloudflared: %w", err)
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		_ = removeCenterRemoteAccessContainer(context.Background(), docker)
		return fmt.Errorf("deployer: start Center cloudflared: %w", err)
	}
	return nil
}

func removeCenterRemoteAccessContainer(ctx context.Context, docker *client.Client) error {
	existing, err := inspectManagedContainer(ctx, docker, centerRemoteAccessContainer, centerRemoteAccessComponent)
	if err != nil || existing == nil {
		return err
	}
	if _, err := docker.ContainerRemove(ctx, existing.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("deployer: remove Center cloudflared: %w", err)
	}
	return nil
}

func centerRemoteAccessCreateOptions(token string) client.ContainerCreateOptions {
	return client.ContainerCreateOptions{
		Config: &container.Config{
			Image: deployapi.CloudflaredImage,
			Cmd:   []string{"tunnel", "--no-autoupdate", "run", "--token", token},
			Labels: map[string]string{
				dockerruntime.ManagedLabel:   "true",
				dockerruntime.ComponentLabel: centerRemoteAccessComponent,
			},
		},
		HostConfig: &container.HostConfig{
			NetworkMode:    container.NetworkMode(dockerruntime.NetworkName),
			RestartPolicy:  container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")},
			ReadonlyRootfs: true,
			SecurityOpt:    []string{"no-new-privileges=true"},
			CapDrop:        []string{"ALL"},
		},
		NetworkingConfig: dockerruntime.NetworkingConfig(centerRemoteAccessAlias),
		Name:             centerRemoteAccessContainer,
	}
}
