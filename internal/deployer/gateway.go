package deployer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/gatewayruntime"
)

type gatewayReplacement struct {
	backupID         string
	legacyID         string
	layer4ID         string
	backupWasRunning bool
	legacyWasRunning bool
	layer4WasRunning bool
	socket           string
}

func (installer DockerHeadscaleInstaller) replaceGateway(ctx context.Context, docker *client.Client, caddyfile []byte) (gatewayReplacement, error) {
	if err := recoverGatewayBackup(ctx, docker); err != nil {
		return gatewayReplacement{}, err
	}
	replacement := gatewayReplacement{socket: installer.CaddyAdminSocket}
	existing, err := inspectManagedContainer(ctx, docker, DefaultGatewayContainer, gatewayruntime.CaddyComponentLabel)
	if err != nil {
		return gatewayReplacement{}, err
	}
	if existing != nil {
		replacement.backupWasRunning = existing.Container.State != nil && existing.Container.State.Running
		if replacement.backupWasRunning {
			if _, err := docker.ContainerStop(ctx, existing.Container.ID, client.ContainerStopOptions{}); err != nil {
				return gatewayReplacement{}, fmt.Errorf("deployer: stop existing unified gateway: %w", err)
			}
		}
		if _, err := docker.ContainerRename(ctx, existing.Container.ID, client.ContainerRenameOptions{NewName: gatewayRollbackContainer}); err != nil {
			return gatewayReplacement{}, fmt.Errorf("deployer: preserve existing unified gateway: %w", err)
		}
		replacement.backupID = existing.Container.ID
	}
	legacy, err := inspectManagedContainer(ctx, docker, gatewayruntime.LegacyCenterCaddyContainer, "center-headscale-gateway")
	if err != nil {
		_ = replacement.rollback(ctx, docker)
		return gatewayReplacement{}, err
	}
	if legacy != nil {
		replacement.legacyID = legacy.Container.ID
		replacement.legacyWasRunning = legacy.Container.State != nil && legacy.Container.State.Running
	}
	layer4, err := inspectManagedContainer(ctx, docker, gatewayruntime.HAProxyContainer, gatewayruntime.Layer4ComponentLabel)
	if err != nil {
		_ = replacement.rollback(ctx, docker)
		return gatewayReplacement{}, err
	}
	if layer4 != nil {
		replacement.layer4ID = layer4.Container.ID
		replacement.layer4WasRunning = layer4.Container.State != nil && layer4.Container.State.Running
	}
	if err := os.MkdirAll(filepath.Dir(installer.CaddyAdminSocket), 0o700); err != nil {
		_ = replacement.rollback(ctx, docker)
		return gatewayReplacement{}, fmt.Errorf("deployer: create Caddy Admin socket directory: %w", err)
	}
	if err := os.Remove(installer.CaddyAdminSocket); err != nil && !os.IsNotExist(err) {
		_ = replacement.rollback(ctx, docker)
		return gatewayReplacement{}, fmt.Errorf("deployer: remove stale Caddy Admin socket: %w", err)
	}
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: installer.CaddyImage,
			Cmd:   []string{"caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"},
			Labels: map[string]string{
				gatewayruntime.ManagedLabel:        "true",
				gatewayruntime.ComponentLabel:      gatewayruntime.CaddyComponentLabel,
				gatewayruntime.SystemServicesLabel: gatewayruntime.SystemServices,
			},
		},
		HostConfig: &container.HostConfig{
			NetworkMode:   container.NetworkMode("host"),
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")},
			Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: installer.CaddyDataVolume, Target: "/data"},
				{Type: mount.TypeVolume, Source: installer.CaddyConfigVolume, Target: "/config"},
				{Type: mount.TypeBind, Source: filepath.Dir(installer.CaddyAdminSocket), Target: filepath.Dir(installer.CaddyAdminSocket)},
			},
		},
		Name: DefaultGatewayContainer,
	})
	if err != nil {
		_ = replacement.rollback(ctx, docker)
		return gatewayReplacement{}, fmt.Errorf("deployer: create unified gateway: %w", err)
	}
	if err := copyFile(ctx, docker, created.ID, "/etc/caddy", "Caddyfile", caddyfile); err != nil {
		_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
		_ = replacement.rollback(ctx, docker)
		return gatewayReplacement{}, err
	}
	if replacement.legacyWasRunning {
		if _, err := docker.ContainerStop(ctx, legacy.Container.ID, client.ContainerStopOptions{}); err != nil {
			_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
			_ = replacement.rollback(ctx, docker)
			return gatewayReplacement{}, fmt.Errorf("deployer: stop legacy Center gateway: %w", err)
		}
	}
	if replacement.layer4WasRunning {
		if _, err := docker.ContainerStop(ctx, layer4.Container.ID, client.ContainerStopOptions{}); err != nil {
			_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
			_ = replacement.rollback(ctx, docker)
			return gatewayReplacement{}, fmt.Errorf("deployer: stop shared-443 frontend for gateway migration: %w", err)
		}
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
		rollbackErr := replacement.rollback(ctx, docker)
		if rollbackErr != nil {
			return gatewayReplacement{}, errors.Join(fmt.Errorf("deployer: start unified gateway: %w", err), rollbackErr)
		}
		return gatewayReplacement{}, fmt.Errorf("deployer: start unified gateway: %w", err)
	}
	return replacement, nil
}

func recoverGatewayBackup(ctx context.Context, docker *client.Client) error {
	backup, err := inspectManagedContainer(ctx, docker, gatewayRollbackContainer, gatewayruntime.CaddyComponentLabel)
	if err != nil || backup == nil {
		return err
	}
	current, err := inspectManagedContainer(ctx, docker, DefaultGatewayContainer, gatewayruntime.CaddyComponentLabel)
	if err != nil {
		return err
	}
	if current != nil {
		if _, err := docker.ContainerRemove(ctx, backup.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			return fmt.Errorf("deployer: remove completed gateway rollback container: %w", err)
		}
		return nil
	}
	if _, err := docker.ContainerRename(ctx, backup.Container.ID, client.ContainerRenameOptions{NewName: DefaultGatewayContainer}); err != nil {
		return fmt.Errorf("deployer: recover preserved unified gateway: %w", err)
	}
	if _, err := docker.ContainerStart(ctx, backup.Container.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("deployer: restart preserved unified gateway: %w", err)
	}
	return nil
}

func (replacement gatewayReplacement) rollback(ctx context.Context, docker *client.Client) error {
	var failures []error
	if current, err := inspectManagedContainer(ctx, docker, DefaultGatewayContainer, gatewayruntime.CaddyComponentLabel); err != nil {
		failures = append(failures, err)
	} else if current != nil {
		if _, err := docker.ContainerRemove(ctx, current.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			failures = append(failures, fmt.Errorf("deployer: remove failed unified gateway: %w", err))
		}
	}
	if replacement.socket != "" {
		if err := os.Remove(replacement.socket); err != nil && !os.IsNotExist(err) {
			failures = append(failures, fmt.Errorf("deployer: clear failed gateway Admin socket: %w", err))
		}
	}
	if replacement.backupID != "" {
		if _, err := docker.ContainerRename(ctx, replacement.backupID, client.ContainerRenameOptions{NewName: DefaultGatewayContainer}); err != nil {
			failures = append(failures, fmt.Errorf("deployer: restore unified gateway name: %w", err))
		} else if replacement.backupWasRunning {
			if _, err := docker.ContainerStart(ctx, replacement.backupID, client.ContainerStartOptions{}); err != nil {
				failures = append(failures, fmt.Errorf("deployer: restart previous unified gateway: %w", err))
			}
		}
	}
	if replacement.legacyID != "" && replacement.legacyWasRunning {
		if _, err := docker.ContainerStart(ctx, replacement.legacyID, client.ContainerStartOptions{}); err != nil {
			failures = append(failures, fmt.Errorf("deployer: restart legacy Center gateway: %w", err))
		}
	}
	if replacement.layer4ID != "" && replacement.layer4WasRunning {
		if _, err := docker.ContainerStart(ctx, replacement.layer4ID, client.ContainerStartOptions{}); err != nil {
			failures = append(failures, fmt.Errorf("deployer: restart shared-443 frontend: %w", err))
		}
	}
	if len(failures) != 0 {
		return errors.Join(failures...)
	}
	return nil
}

func (replacement gatewayReplacement) commit(ctx context.Context, docker *client.Client) error {
	for _, id := range []string{replacement.backupID, replacement.legacyID, replacement.layer4ID} {
		if id == "" {
			continue
		}
		if _, err := docker.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("deployer: finalize unified gateway migration: %w", err)
		}
	}
	return nil
}
