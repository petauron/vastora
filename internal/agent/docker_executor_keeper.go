package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

type keeperConfig struct {
	Timezone string `json:"timezone"`
}

type keeperSecrets struct {
	LoginPassword    string `json:"login_password"`
	CPAManagementKey string `json:"cpa_management_key"`
}

func decodeKeeperConfig(rawConfig, rawSecrets json.RawMessage) (keeperConfig, keeperSecrets, error) {
	var config keeperConfig
	var secrets keeperSecrets
	if json.Unmarshal(rawConfig, &config) != nil || json.Unmarshal(rawSecrets, &secrets) != nil || config.Timezone == "" || secrets.LoginPassword == "" || secrets.CPAManagementKey == "" {
		return config, secrets, errors.New("agent: incomplete Keeper configuration")
	}
	return config, secrets, nil
}

func deployKeeper(ctx context.Context, docker *client.Client, task DeploymentTask, bindAddress string) error {
	expectedVersion, official := OfficialAppVersion("keeper")
	if task.Manifest.ID != "keeper" || !official || task.Manifest.Version != expectedVersion {
		return errors.New("agent: unsupported Keeper package")
	}
	imageRef, err := pullDeclaredImage(ctx, docker, task, "keeper")
	if err != nil {
		return err
	}
	config, secrets, err := decodeKeeperConfig(task.Config, task.Secrets)
	if err != nil {
		return err
	}
	if err := ensureCPANetwork(ctx, docker); err != nil {
		return err
	}
	if err := ensureOwnedApplicationVolumes(ctx, docker, applicationVolumes[keeperKey], keeperKey, task.ApplicationID); err != nil {
		return err
	}
	if err := removeOwnedApplicationContainer(ctx, docker, keeperContainer, keeperKey, "keeper", task.ApplicationID, anyApplicationDeployment); err != nil {
		return err
	}
	keeperPort := network.MustParsePort("8080/tcp")
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: imageRef, Labels: applicationResourceLabels(keeperKey, "keeper", task.ApplicationID, task.ID), Env: []string{"TZ=" + config.Timezone, "CPA_BASE_URL=http://" + cpaContainer + ":8317", "CPA_MANAGEMENT_KEY=" + secrets.CPAManagementKey, "LOGIN_PASSWORD=" + secrets.LoginPassword, "AUTH_ENABLED=true"}, ExposedPorts: network.PortSet{keeperPort: struct{}{}}},
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
