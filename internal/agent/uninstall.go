package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/gatewayruntime"
)

// PurgeManagedRuntime removes only workloads with fixed Vastora ownership. It
// is intentionally independent of Center and Agent database availability so a
// partially damaged node can still be cleaned locally.
func PurgeManagedRuntime(ctx context.Context, deleteApplicationData bool) error {
	var result error
	gatewaySettings, err := (DockerGatewayProvisioner{}).settings()
	if err != nil {
		return err
	}
	docker, err := client.New(client.WithHost("unix:///var/run/docker.sock"))
	if err != nil {
		return fmt.Errorf("agent: connect Docker to inspect gateway data: %w", err)
	}
	defer docker.Close()
	existingGateway, err := inspectManagedCaddy(ctx, docker, gatewaySettings.Container)
	if err != nil {
		return err
	}
	for _, volume := range []string{gatewaySettings.DataVolume, gatewaySettings.ConfigVolume} {
		if err := verifyManagedGatewayVolume(ctx, docker, existingGateway, volume); err != nil {
			return err
		}
	}
	executor := DockerExecutor{}
	for _, appKey := range []string{keeperKey, komariKey, cpaKey, threeXUIKey} {
		_, err := executor.Deploy(ctx, DeploymentTask{AppKey: appKey, Operation: "uninstall", DeleteData: deleteApplicationData})
		result = errors.Join(result, err)
	}
	result = errors.Join(result, (DockerTunnelProvisioner{}).Apply(ctx, TunnelDesiredState{Revision: 1, Status: "stopped"}))
	result = errors.Join(result, (ManagedGatewayProvisioner{Caddy: DockerGatewayProvisioner{}, Layer4: DockerLayer4Provisioner{}}).Remove(ctx))
	if result != nil {
		return result
	}
	for _, volume := range []string{gatewaySettings.DataVolume, gatewaySettings.ConfigVolume} {
		if _, err := docker.VolumeRemove(ctx, volume, client.VolumeRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("agent: remove gateway volume %s: %w", volume, err)
		}
	}
	return nil
}

func verifyManagedGatewayVolume(ctx context.Context, docker *client.Client, gateway *client.ContainerInspectResult, name string) error {
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
	if gateway != nil {
		for _, mount := range gateway.Container.Mounts {
			if mount.Name == name {
				return nil
			}
		}
	}
	return fmt.Errorf("agent: refusing to remove unowned gateway volume %s", name)
}
