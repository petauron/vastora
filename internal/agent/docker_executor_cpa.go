package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
)

type cpaConfig struct {
	Timezone string `json:"timezone"`
	Debug    bool   `json:"debug"`
}

type cpaSecrets struct {
	ManagementKey string `json:"management_key"`
	APIKey        string `json:"api_key"`
}

func deployCPA(ctx context.Context, docker *client.Client, task DeploymentTask, bindAddress string) error {
	if task.Manifest.ID != "cpa" || task.Manifest.Version != "7.2.128" {
		return errors.New("agent: unsupported official CPA package")
	}
	imageRef, err := pullDeclaredImage(ctx, docker, task, "cli-proxy-api")
	if err != nil {
		return err
	}
	settings, secrets, err := decodeCPAConfig(task.Config, task.Secrets)
	if err != nil {
		return err
	}
	if err := ensureCPANetwork(ctx, docker); err != nil {
		return err
	}
	if err := ensureOwnedApplicationVolumes(ctx, docker, applicationVolumes[cpaKey], cpaKey, task.ApplicationID); err != nil {
		return err
	}
	if err := removeOwnedApplicationContainer(ctx, docker, cpaContainer, cpaKey, "cpa", task.ApplicationID, anyApplicationDeployment); err != nil {
		return err
	}

	port := network.MustParsePort("8317/tcp")
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:        imageRef,
			Labels:       applicationResourceLabels(cpaKey, "cpa", task.ApplicationID, task.ID),
			Env:          []string{"TZ=" + settings.Timezone},
			ExposedPorts: network.PortSet{port: struct{}{}},
			WorkingDir:   "/CLIProxyAPI",
		},
		HostConfig: &container.HostConfig{
			NetworkMode:   container.NetworkMode(cpaNetwork),
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")},
			PortBindings:  network.PortMap{port: []network.PortBinding{{HostIP: netip.MustParseAddr(bindAddress), HostPort: "8317"}}},
			Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: "vastora-cpa-auths", Target: "/root/.cli-proxy-api"},
				{Type: mount.TypeVolume, Source: "vastora-cpa-logs", Target: "/CLIProxyAPI/logs"},
				{Type: mount.TypeVolume, Source: "vastora-cpa-plugins", Target: "/CLIProxyAPI/plugins"},
			},
		},
		Name: cpaContainer,
	})
	if err != nil {
		return fmt.Errorf("agent: create CPA container: %w", err)
	}
	if err := copyCPAConfig(ctx, docker, created.ID, settings, secrets); err != nil {
		_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
		return err
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("agent: start CPA container: %w", err)
	}
	inspected, err := docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("agent: inspect CPA container: %w", err)
	}
	if inspected.Container.State == nil || !inspected.Container.State.Running {
		return errors.New("agent: CPA container did not remain running")
	}
	return nil
}

func decodeCPAConfig(rawConfig, rawSecrets json.RawMessage) (cpaConfig, cpaSecrets, error) {
	var config cpaConfig
	var secrets cpaSecrets
	if json.Unmarshal(rawConfig, &config) != nil || json.Unmarshal(rawSecrets, &secrets) != nil {
		return config, secrets, errors.New("agent: invalid CPA configuration")
	}
	if config.Timezone == "" || secrets.ManagementKey == "" || secrets.APIKey == "" {
		return config, secrets, errors.New("agent: incomplete CPA configuration")
	}
	return config, secrets, nil
}

func copyCPAConfig(ctx context.Context, docker *client.Client, containerID string, settings cpaConfig, secrets cpaSecrets) error {
	payload, err := json.Marshal(map[string]any{
		"host": "0.0.0.0", "port": 8317, "auth-dir": "/root/.cli-proxy-api", "api-keys": []string{secrets.APIKey}, "debug": settings.Debug,
		"logging-to-file":   true,
		"remote-management": map[string]any{"allow-remote": true, "secret-key": secrets.ManagementKey},
	})
	if err != nil {
		return fmt.Errorf("agent: encode CPA configuration: %w", err)
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "config.yaml", Mode: 0o600, Size: int64(len(payload))}); err != nil {
		return fmt.Errorf("agent: archive CPA configuration: %w", err)
	}
	if _, err := writer.Write(payload); err != nil {
		return fmt.Errorf("agent: archive CPA configuration: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("agent: archive CPA configuration: %w", err)
	}
	if _, err := docker.CopyToContainer(ctx, containerID, client.CopyToContainerOptions{DestinationPath: "/CLIProxyAPI", Content: &archive}); err != nil {
		return fmt.Errorf("agent: write CPA configuration: %w", err)
	}
	return nil
}

func ensureCPANetwork(ctx context.Context, docker *client.Client) error {
	return dockerruntime.EnsureBridgeNetwork(ctx, docker, cpaNetwork, "cpa-network")
}
