package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

func deployKeeper(ctx context.Context, docker *client.Client, task DeploymentTask, bindAddress string) error {
	if task.Manifest.ID != "keeper" || task.Manifest.Version != "1.14.1" {
		return errors.New("agent: unsupported Keeper package")
	}
	imageRef, err := declaredImage(task.Manifest, "keeper")
	if err != nil {
		return err
	}
	var config struct {
		Timezone string `json:"timezone"`
	}
	var secrets struct {
		LoginPassword    string `json:"login_password"`
		CPAManagementKey string `json:"cpa_management_key"`
	}
	if json.Unmarshal(task.Config, &config) != nil || json.Unmarshal(task.Secrets, &secrets) != nil || config.Timezone == "" || secrets.LoginPassword == "" || secrets.CPAManagementKey == "" {
		return errors.New("agent: incomplete Keeper configuration")
	}
	if err := ensureCPANetwork(ctx, docker); err != nil {
		return err
	}
	pull, err := docker.ImagePull(ctx, imageRef, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("agent: pull Keeper image: %w", err)
	}
	_, _ = io.Copy(io.Discard, pull)
	_ = pull.Close()
	_, _ = docker.ContainerRemove(ctx, keeperContainer, client.ContainerRemoveOptions{Force: true})
	keeperPort := network.MustParsePort("8080/tcp")
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: imageRef, Env: []string{"TZ=" + config.Timezone, "CPA_BASE_URL=http://" + cpaContainer + ":8317", "CPA_MANAGEMENT_KEY=" + secrets.CPAManagementKey, "LOGIN_PASSWORD=" + secrets.LoginPassword, "AUTH_ENABLED=true"}, ExposedPorts: network.PortSet{keeperPort: struct{}{}}},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(cpaNetwork), RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")}, PortBindings: network.PortMap{keeperPort: []network.PortBinding{{HostIP: netip.MustParseAddr(bindAddress), HostPort: "8080"}}}, Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: "vastora-cpa-keeper-data", Target: "/data"}}},
		Name:       keeperContainer,
	})
	if err != nil {
		return fmt.Errorf("agent: create Keeper container: %w", err)
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("agent: start Keeper container: %w", err)
	}
	inspected, err := docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("agent: inspect Keeper container: %w", err)
	}
	if inspected.Container.State == nil || !inspected.Container.State.Running {
		return errors.New("agent: Keeper container did not remain running")
	}
	return nil
}
