package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/gatewayruntime"
)

// PurgeManagedRuntime removes only workloads with fixed Vastora ownership. It
// is intentionally independent of Center and Agent database availability so a
// partially damaged node can still be cleaned locally.
func PurgeManagedRuntime(ctx context.Context, packageStateDirectory string, deleteApplicationData bool, authorize func(context.Context, string) error) error {
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
	packageSteps, err := packagePurgeSteps(packageStateDirectory, deleteApplicationData, docker)
	if err != nil {
		return err
	}
	steps = append(steps, packageSteps...)
	for _, path := range []string{komariUnitPath, pulseUnitPath} {
		if _, err := os.Lstat(path); err == nil {
			owned := false
			for _, step := range packageSteps {
				owned = owned || strings.Contains(step.name, path)
			}
			if !owned {
				return errors.New("agent: adopt historical native application before removing the Agent")
			}
		}
	}
	steps = append(steps,
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

func packagePurgeSteps(directory string, deleteData bool, docker packageDockerEngine) ([]runtimeCleanupStep, error) {
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(canonical, "packages"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	executor := PackageExecutor{StateDirectory: canonical}
	var steps []runtimeCleanupStep
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "vastora-pkg-") {
			return nil, errors.New("agent: unrecognized package state requires review")
		}
		raw, err := os.ReadFile(filepath.Join(canonical, "packages", entry.Name(), "resources.json"))
		if err != nil {
			return nil, errors.New("agent: package ownership receipt missing")
		}
		var identifier InstanceResources
		if json.Unmarshal(raw, &identifier) != nil {
			return nil, errors.New("agent: invalid package ownership receipt")
		}
		receipt, err := executor.ReadResources(identifier.ApplicationID)
		if err != nil || entry.Name() != packageIdentity(identifier.ApplicationID) {
			return nil, errors.New("agent: package namespace mismatch")
		}
		if receipt.State == "removed" {
			continue
		}
		if receipt.State != "ready" && receipt.State != "retained" {
			return nil, errors.New("agent: unfinished package execution prevents Agent removal")
		}
		var backend PackageBackend
		switch receipt.Runtime {
		case "docker":
			backend = &DockerPackageBackend{Docker: docker, StateDirectory: canonical}
		case "systemd":
			backend = &SystemdPackageBackend{StateDirectory: canonical}
		default:
			return nil, errors.New("agent: unknown package runtime prevents Agent removal")
		}
		name := receipt.AppKey
		for _, resource := range receipt.Resources {
			if resource.Kind == "unit" {
				name += " " + resource.Path
			}
		}
		steps = append(steps, runtimeCleanupStep{name, func(ctx context.Context) error {
			task := DeploymentTask{ID: "agent-uninstall", ApplicationID: receipt.ApplicationID, AppKey: receipt.AppKey, Operation: "uninstall", DeleteData: deleteData}
			if err := backend.Inspect(ctx, task, receipt); err != nil {
				return err
			}
			receipt.State, receipt.TaskID = "removing", task.ID
			if err := executor.save(receipt); err != nil {
				return err
			}
			if err := backend.Remove(ctx, task, receipt); err != nil {
				receipt.State = "review-required"
				return errors.Join(err, executor.save(receipt))
			}
			receipt.State = "removed"
			if len(receipt.Resources) > 0 {
				receipt.State = "retained"
			}
			return executor.save(receipt)
		}})
	}
	return steps, nil
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
