package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/gatewayruntime"
)

type gatewayAttachmentEngine interface {
	dockerruntime.AttachmentEngine
	ContainerStart(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerRestart(context.Context, string, client.ContainerRestartOptions) (client.ContainerRestartResult, error)
}

// RecoverStartupRuntime restores committed local runtime resources once,
// before control loops. It does not replace containers, advance a revision,
// replay tasks or change existing application configuration.
func (s *Store) RecoverStartupRuntime(ctx context.Context) error {
	s.gatewayMutationMu.Lock()
	defer s.gatewayMutationMu.Unlock()
	docker, err := (DockerGatewayProvisioner{}).client()
	if err != nil {
		return err
	}
	defer docker.Close()
	bootstrap, err := s.beginCohostPrivateBootstrap(ctx, docker)
	if err != nil {
		return err
	}
	defer func() { _ = bootstrap.close(context.WithoutCancel(ctx)) }()
	var failures []error
	for _, runtime := range []struct{ name, component, alias string }{
		{defaultCaddyContainer, gatewayruntime.CaddyComponentLabel, dockerruntime.CaddyAlias},
		{defaultHAProxyContainer, gatewayruntime.Layer4ComponentLabel, dockerruntime.HAProxyAlias},
	} {
		if err := recoverGatewayAttachment(ctx, docker, runtime.name, runtime.component, runtime.alias); err != nil {
			failures = append(failures, fmt.Errorf("agent: recover %s network attachment: %w", runtime.name, err))
		}
	}
	if len(failures) == 0 {
		current, inspectErr := docker.ContainerInspect(ctx, defaultCaddyContainer, client.ContainerInspectOptions{})
		if inspectErr != nil && !errdefs.IsNotFound(inspectErr) {
			failures = append(failures, inspectErr)
		}
		if inspectErr == nil && current.Container.State != nil && current.Container.State.Running {
			if err := s.restoreGatewayConfiguration(ctx); err != nil {
				failures = append(failures, err)
			}
		}
	}
	if len(failures) == 0 {
		if bootstrap != nil && s.linkChecker == nil {
			failures = append(failures, errors.New("agent: private control identity checker is unavailable"))
		} else if err := bootstrap.waitForTailscale(ctx, s.linkChecker); err != nil {
			failures = append(failures, err)
		}
	}
	if err := bootstrap.close(context.WithoutCancel(ctx)); err != nil {
		failures = append(failures, err)
	}
	if len(failures) == 0 {
		if err := s.recoverPackageNetworks(ctx, docker); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func recoverGatewayAttachment(ctx context.Context, docker gatewayAttachmentEngine, name, component, alias string) error {
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
	// Normal stops and unknown failures do not authorize a start. Docker records
	// its own EADDRNOTAVAIL failure separately from an application exit.
	if !value.State.Running && !gatewayBindStartFailed(current) {
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
	if !value.State.Running {
		if err := waitForPublishedAddresses(ctx, value.HostConfig.PortBindings); err != nil {
			return err
		}
		if err := dockerruntime.ValidateExistingBridgeNetwork(ctx, docker, dockerruntime.NetworkName, "runtime-network"); err != nil {
			return err
		}
		_, startErr := docker.ContainerStart(ctx, value.ID, client.ContainerStartOptions{})
		started, inspectErr := docker.ContainerInspect(ctx, value.ID, client.ContainerInspectOptions{})
		if inspectErr != nil || started.Container.State == nil || !started.Container.State.Running {
			return errors.Join(errors.New("gateway did not resume after its bind address became ready"), startErr, inspectErr)
		}
		// A lost start response is settled by this immutable-ID readback. Never
		// repeat the start or replace the container based on that response.
		current = started
	}
	if dockerruntime.AttachmentHealthy(current, dockerruntime.NetworkName, alias) {
		return nil
	}
	if err := waitForPublishedAddresses(ctx, value.HostConfig.PortBindings); err != nil {
		return err
	}
	return dockerruntime.RecoverAttachment(ctx, docker, value.ID, dockerruntime.NetworkName, "runtime-network", alias)
}

func gatewayBindStartFailed(current client.ContainerInspectResult) bool {
	value := current.Container
	if value.State == nil || value.State.Running || value.State.Status != "exited" || value.State.OOMKilled ||
		value.HostConfig == nil || value.HostConfig.AutoRemove || value.HostConfig.RestartPolicy.Name != "unless-stopped" ||
		!strings.Contains(value.State.Error, "cannot assign requested address") {
		return false
	}
	for _, bindings := range value.HostConfig.PortBindings {
		for _, bind := range bindings {
			if bind.HostIP.IsValid() && !bind.HostIP.IsUnspecified() &&
				strings.Contains(value.State.Error, "failed to bind host port "+net.JoinHostPort(bind.HostIP.String(), bind.HostPort)+"/") {
				return true
			}
		}
	}
	return false
}
