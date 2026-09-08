package agent

import (
	"context"
	"errors"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
)

// The instance stays available for its local management API while its business
// route is kernel-blocked. Restart terminates old streams without stopping the
// controller or HAProxy. Docker's automatic restart must remain disabled until
// the original direct configuration has been restored and read back.
type landingDocker struct {
	engine                     *client.Client
	applicationID, containerID string
}

func openLandingDocker(ctx context.Context, applicationID, expectedContainerID string) (*landingDocker, string, string, error) {
	docker, err := client.New(client.WithHost("unix:///var/run/docker.sock"))
	if err != nil {
		return nil, "", "", errors.New("agent: Docker is unavailable for landing configuration")
	}
	failed := true
	defer func() {
		if failed {
			_ = docker.Close()
		}
	}()
	inspected, err := docker.ContainerInspect(ctx, threeXUIContainer, client.ContainerInspectOptions{})
	if err != nil || inspected.Container.Config == nil || inspected.Container.HostConfig == nil || inspected.Container.NetworkSettings == nil {
		return nil, "", "", errors.New("agent: selected proxy instance is unavailable")
	}
	value := inspected.Container
	if expectedContainerID != "" && value.ID != expectedContainerID {
		return nil, "", "", errors.New("agent: selected proxy instance changed")
	}
	if err := validateApplicationResourceLabels(value.Config.Labels, threeXUIKey, "3x-ui", applicationID, anyApplicationDeployment); err != nil {
		return nil, "", "", err
	}
	if string(value.HostConfig.NetworkMode) != dockerruntime.NetworkName || len(value.NetworkSettings.Networks) != 1 {
		return nil, "", "", errors.New("agent: landing requires the managed proxy bridge")
	}
	endpoint := value.NetworkSettings.Networks[dockerruntime.NetworkName]
	if endpoint == nil || endpoint.NetworkID == "" {
		return nil, "", "", errors.New("agent: proxy bridge attachment is missing")
	}
	network, err := docker.NetworkInspect(ctx, endpoint.NetworkID, client.NetworkInspectOptions{})
	if err != nil {
		return nil, "", "", errors.New("agent: proxy bridge is unavailable")
	}
	bridgeNetwork := network.Network
	if bridgeNetwork.Driver != "bridge" || bridgeNetwork.Name != dockerruntime.NetworkName || bridgeNetwork.Labels[dockerruntime.ManagedLabel] != "true" || bridgeNetwork.Labels[dockerruntime.ComponentLabel] != "runtime-network" {
		return nil, "", "", errors.New("agent: proxy bridge ownership changed")
	}
	bridge := bridgeNetwork.Options["com.docker.network.bridge.name"]
	if bridge == "" {
		if len(bridgeNetwork.ID) < 12 {
			return nil, "", "", errors.New("agent: invalid proxy bridge identity")
		}
		bridge = "br-" + bridgeNetwork.ID[:12]
	}
	policy := string(value.HostConfig.RestartPolicy.Name)
	if policy != "no" && policy != "always" && policy != "unless-stopped" {
		return nil, "", "", errors.New("agent: unsupported proxy restart policy")
	}
	failed = false
	return &landingDocker{engine: docker, applicationID: applicationID, containerID: value.ID}, bridge, policy, nil
}

func (d *landingDocker) inspect(ctx context.Context) (client.ContainerInspectResult, error) {
	value, err := d.engine.ContainerInspect(ctx, d.containerID, client.ContainerInspectOptions{})
	if err != nil || value.Container.ID != d.containerID || value.Container.Config == nil || value.Container.HostConfig == nil || value.Container.State == nil {
		return value, errors.New("agent: proxy instance identity is unavailable")
	}
	if err := validateApplicationResourceLabels(value.Container.Config.Labels, threeXUIKey, "3x-ui", d.applicationID, anyApplicationDeployment); err != nil {
		return value, err
	}
	return value, nil
}

func (d *landingDocker) restartPolicy(ctx context.Context, policy string) error {
	if policy != "no" && policy != "always" && policy != "unless-stopped" {
		return errors.New("agent: invalid proxy restart policy")
	}
	current, err := d.inspect(ctx)
	if err != nil {
		return err
	}
	if string(current.Container.HostConfig.RestartPolicy.Name) == policy {
		return nil
	}
	_, writeErr := d.engine.ContainerUpdate(ctx, d.containerID, client.ContainerUpdateOptions{RestartPolicy: &container.RestartPolicy{Name: container.RestartPolicyMode(policy)}})
	actual, readErr := d.inspect(ctx)
	if readErr == nil && string(actual.Container.HostConfig.RestartPolicy.Name) == policy {
		return nil
	}
	return errors.Join(errors.New("agent: proxy restart policy was not confirmed"), writeErr, readErr)
}

// Caller must close the gate first. Keep a running management API available
// during reconciliation; a rejected configuration must not restart it.
func (d *landingDocker) startForReconciliation(ctx context.Context) error {
	current, err := d.inspect(ctx)
	if err != nil {
		return err
	}
	if current.Container.HostConfig.RestartPolicy.Name != "no" {
		return errors.New("agent: proxy automatic restart is not fenced")
	}
	if current.Container.State.Running {
		return nil
	}
	_, err = d.engine.ContainerStart(ctx, d.containerID, client.ContainerStartOptions{})
	return err
}

// Caller must install/block the gate before invoking this method. Stop/readback
// is separate from start so a lost Docker reply never counts as stream closure.
func (d *landingDocker) terminateConnections(ctx context.Context) error {
	current, err := d.inspect(ctx)
	if err != nil {
		return err
	}
	if current.Container.HostConfig.RestartPolicy.Name != "no" {
		return errors.New("agent: proxy automatic restart is not fenced")
	}
	if current.Container.State.Running {
		timeout := 0
		_, stopErr := d.engine.ContainerStop(ctx, d.containerID, client.ContainerStopOptions{Timeout: &timeout})
		stopped, readErr := d.inspect(ctx)
		if readErr != nil || stopped.Container.State.Running {
			return errors.Join(errors.New("agent: proxy connections did not stop"), stopErr, readErr)
		}
	}
	_, startErr := d.engine.ContainerStart(ctx, d.containerID, client.ContainerStartOptions{})
	started, readErr := d.inspect(ctx)
	if readErr != nil || !started.Container.State.Running {
		return errors.Join(errors.New("agent: proxy instance did not restart"), startErr, readErr)
	}
	return nil
}
