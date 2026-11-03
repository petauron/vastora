package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/gatewayruntime"
)

// RecoverGatewayAttachments restores only the network of an already running,
// owned gateway. It never starts, replaces or configures a container, or replays
// a saved gateway revision. Call once before starting the control/task loops.
func (s *Store) RecoverGatewayAttachments(ctx context.Context) error {
	s.gatewayMutationMu.Lock()
	defer s.gatewayMutationMu.Unlock()
	docker, err := (DockerGatewayProvisioner{}).client()
	if err != nil {
		return err
	}
	defer docker.Close()
	var failures []error
	for _, runtime := range []struct{ name, component, alias string }{
		{defaultCaddyContainer, gatewayruntime.CaddyComponentLabel, dockerruntime.CaddyAlias},
		{defaultHAProxyContainer, gatewayruntime.Layer4ComponentLabel, dockerruntime.HAProxyAlias},
	} {
		if err := recoverRunningGatewayAttachment(ctx, docker, runtime.name, runtime.component, runtime.alias); err != nil {
			failures = append(failures, fmt.Errorf("agent: recover %s network attachment: %w", runtime.name, err))
		}
	}
	return errors.Join(failures...)
}

func recoverRunningGatewayAttachment(ctx context.Context, docker dockerruntime.AttachmentEngine, name, component, alias string) error {
	current, err := docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	value := current.Container
	if value.State == nil {
		return errors.New("container state is unavailable")
	}
	// An intentional stop must remain stopped, including retained old workers.
	if !value.State.Running {
		return nil
	}
	if value.ID == "" || value.Config == nil || value.HostConfig == nil ||
		value.Config.Labels[dockerruntime.ManagedLabel] != "true" ||
		value.Config.Labels[dockerruntime.ComponentLabel] != component ||
		string(value.HostConfig.NetworkMode) != dockerruntime.NetworkName {
		return errors.New("refusing to recover an unowned or mismatched gateway")
	}
	if value.State.Paused || value.State.Restarting || value.State.Dead {
		return errors.New("gateway is not stably running")
	}
	if value.NetworkSettings != nil {
		for network := range value.NetworkSettings.Networks {
			if network != dockerruntime.NetworkName {
				return errors.New("refusing to change a gateway with additional network attachments")
			}
		}
	}
	if dockerruntime.AttachmentHealthy(current, dockerruntime.NetworkName, alias) {
		return nil
	}
	if err := waitForPublishedAddresses(ctx, value.HostConfig.PortBindings); err != nil {
		return err
	}
	return dockerruntime.RecoverAttachment(ctx, docker, value.ID, dockerruntime.NetworkName, "runtime-network", alias)
}
