package agent

import (
	"context"
	"fmt"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/gatewayruntime"
)

// PurgeManagedRuntime removes only workloads with fixed Vastora ownership. It
// is intentionally independent of Center and Agent database availability so a
// partially damaged node can still be cleaned locally.
func PurgeManagedRuntime(ctx context.Context, deleteApplicationData bool, authorize func(context.Context, string) error) error {
	gatewaySettings, err := (DockerGatewayProvisioner{}).settings()
	if err != nil {
		return err
	}
	docker, err := client.New(client.WithHost("unix:///var/run/docker.sock"))
	if err != nil {
		return fmt.Errorf("agent: connect Docker to inspect gateway data: %w", err)
	}
	defer docker.Close()
	for _, volume := range []string{gatewaySettings.DataVolume, gatewaySettings.ConfigVolume} {
		if err := verifyManagedGatewayVolume(ctx, docker, volume); err != nil {
			return err
		}
	}
	var steps []runtimeCleanupStep
	for _, appKey := range []string{keeperKey, cpaKey, threeXUIKey} {
		steps = append(steps, runtimeCleanupStep{appKey, func(ctx context.Context) error {
			return uninstallDockerApp(ctx, docker, appKey, "", deleteApplicationData)
		}})
	}
	steps = append(steps,
		runtimeCleanupStep{"host probe", (SystemdHostApplicationManager{}).RemoveKomari},
		runtimeCleanupStep{"tunnel", func(ctx context.Context) error {
			return (DockerTunnelProvisioner{}).Apply(ctx, TunnelDesiredState{Revision: 1, Status: "stopped"})
		}},
		runtimeCleanupStep{"gateway", (ManagedGatewayProvisioner{Caddy: DockerGatewayProvisioner{}, Layer4: DockerLayer4Provisioner{}}).Remove},
		runtimeCleanupStep{"layer4", (DockerLayer4Provisioner{}).Remove},
	)
	for _, volume := range []string{gatewaySettings.DataVolume, gatewaySettings.ConfigVolume} {
		steps = append(steps, runtimeCleanupStep{"gateway volume", func(ctx context.Context) error {
			if _, err := docker.VolumeRemove(ctx, volume, client.VolumeRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
				return err
			}
			return nil
		}})
	}
	return runRuntimeCleanupSteps(ctx, authorize, steps)
}

type runtimeCleanupStep struct {
	name string
	run  func(context.Context) error
}

func runRuntimeCleanupSteps(ctx context.Context, authorize func(context.Context, string) error, steps []runtimeCleanupStep) error {
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if authorize != nil {
			if err := authorize(ctx, "runtime"); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := step.run(ctx); err != nil {
			return fmt.Errorf("agent: remove %s: %w", step.name, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func verifyManagedGatewayVolume(ctx context.Context, docker *client.Client, name string) error {
	volume, err := docker.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("agent: inspect gateway volume %s: %w", name, err)
	}
	if volume.Volume.Labels[gatewayruntime.ManagedLabel] == "true" && volume.Volume.Labels[gatewayruntime.ComponentLabel] == "gateway-storage" {
		return nil
	}
	return fmt.Errorf("agent: refusing to remove unowned gateway volume %s", name)
}
