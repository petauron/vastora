package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func deployKomari(ctx context.Context, docker *client.Client, task DeploymentTask) error {
	if task.Manifest.ID != "komari-agent" || task.Manifest.Version != "1.2.60" {
		return errors.New("agent: unsupported Komari Agent package")
	}
	imageRef, err := declaredImage(task.Manifest, "komari-agent")
	if err != nil {
		return err
	}
	var config struct {
		Endpoint string `json:"endpoint"`
	}
	var secrets struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(task.Config, &config) != nil || json.Unmarshal(task.Secrets, &secrets) != nil || config.Endpoint == "" || secrets.Token == "" {
		return errors.New("agent: incomplete Komari Agent configuration")
	}
	pull, err := docker.ImagePull(ctx, imageRef, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("agent: pull Komari Agent image: %w", err)
	}
	_, _ = io.Copy(io.Discard, pull)
	_ = pull.Close()
	_, _ = docker.ContainerRemove(ctx, komariContainer, client.ContainerRemoveOptions{Force: true})
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: imageRef, Cmd: []string{"--endpoint", config.Endpoint, "--token", secrets.Token, "--disable-web-ssh"}},
		HostConfig: &container.HostConfig{NetworkMode: "host", PidMode: "host", RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")}, Binds: []string{"/proc:/host/proc:ro", "/sys:/host/sys:ro", "/:/host:ro"}},
		Name:       komariContainer,
	})
	if err != nil {
		return fmt.Errorf("agent: create Komari Agent container: %w", err)
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("agent: start Komari Agent container: %w", err)
	}
	inspected, err := docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("agent: inspect Komari Agent container: %w", err)
	}
	if inspected.Container.State == nil || !inspected.Container.State.Running {
		return errors.New("agent: Komari Agent container did not remain running")
	}
	return nil
}
