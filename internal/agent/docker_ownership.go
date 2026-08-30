package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
)

const (
	applicationIdentityLabel     = "io.vastora.application"
	applicationInstallationLabel = "io.vastora.application.id"
	anyApplicationDeployment     = "*"
)

func applicationResourceLabels(appKey, component, applicationID, deploymentID string) map[string]string {
	labels := map[string]string{
		dockerruntime.ManagedLabel:   "true",
		dockerruntime.ComponentLabel: component,
		applicationIdentityLabel:     appKey,
		applicationInstallationLabel: applicationID,
	}
	if deploymentID != "" {
		labels[applicationDeploymentIDLabel] = deploymentID
	}
	return labels
}

func validateApplicationResourceLabels(labels map[string]string, appKey, component, applicationID, deploymentID string) error {
	if labels[dockerruntime.ManagedLabel] != "true" || labels[dockerruntime.ComponentLabel] != component || labels[applicationIdentityLabel] != appKey {
		return errors.New("ownership labels do not match")
	}
	actualApplicationID := strings.TrimSpace(labels[applicationInstallationLabel])
	if actualApplicationID == "" || applicationID != "" && actualApplicationID != applicationID {
		return errors.New("application identity does not match")
	}
	actualDeploymentID := strings.TrimSpace(labels[applicationDeploymentIDLabel])
	if (deploymentID == anyApplicationDeployment && actualDeploymentID == "") || (deploymentID != "" && deploymentID != anyApplicationDeployment && actualDeploymentID != deploymentID) {
		return errors.New("deployment identity does not match")
	}
	return nil
}

func inspectOwnedApplicationContainer(ctx context.Context, docker interface {
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
}, name, appKey, component, applicationID, deploymentID string) (client.ContainerInspectResult, bool, error) {
	inspected, err := docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return client.ContainerInspectResult{}, false, nil
	}
	if err != nil {
		return client.ContainerInspectResult{}, false, fmt.Errorf("agent: inspect Docker container %s: %w", name, err)
	}
	if inspected.Container.Config == nil || validateApplicationResourceLabels(inspected.Container.Config.Labels, appKey, component, applicationID, deploymentID) != nil {
		return client.ContainerInspectResult{}, false, fmt.Errorf("agent: refusing to use unowned Docker container %s", name)
	}
	return inspected, true, nil
}

func removeOwnedApplicationContainer(ctx context.Context, docker interface {
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerRemove(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
}, name, appKey, component, applicationID, deploymentID string) error {
	inspected, exists, err := inspectOwnedApplicationContainer(ctx, docker, name, appKey, component, applicationID, deploymentID)
	if err != nil || !exists {
		return err
	}
	if _, err := docker.ContainerRemove(ctx, inspected.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		if _, stillExists, inspectErr := inspectOwnedApplicationContainer(ctx, docker, name, appKey, component, applicationID, deploymentID); inspectErr == nil && !stillExists {
			return nil
		}
		return fmt.Errorf("agent: remove owned Docker container %s: %w", name, err)
	}
	return nil
}

func ensureOwnedApplicationVolume(ctx context.Context, docker interface {
	VolumeInspect(context.Context, string, client.VolumeInspectOptions) (client.VolumeInspectResult, error)
	VolumeCreate(context.Context, client.VolumeCreateOptions) (client.VolumeCreateResult, error)
}, name, appKey, component, applicationID string) error {
	inspected, err := docker.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if err == nil {
		if validateApplicationResourceLabels(inspected.Volume.Labels, appKey, component, applicationID, "") != nil {
			return fmt.Errorf("agent: refusing to use unowned Docker volume %s", name)
		}
		return nil
	}
	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("agent: inspect Docker volume %s: %w", name, err)
	}
	if _, err := docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Labels: applicationResourceLabels(appKey, component, applicationID, "")}); err != nil {
		created, inspectErr := docker.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
		if inspectErr == nil && validateApplicationResourceLabels(created.Volume.Labels, appKey, component, applicationID, "") == nil {
			return nil
		}
		if errdefs.IsAlreadyExists(err) {
			return fmt.Errorf("agent: refusing to use concurrently created unowned Docker volume %s", name)
		}
		return fmt.Errorf("agent: create Docker volume %s: %w", name, err)
	}
	created, inspectErr := docker.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if inspectErr != nil || validateApplicationResourceLabels(created.Volume.Labels, appKey, component, applicationID, "") != nil {
		return fmt.Errorf("agent: refusing to use created Docker volume %s", name)
	}
	return nil
}

func ensureOwnedApplicationVolumes(ctx context.Context, docker interface {
	VolumeInspect(context.Context, string, client.VolumeInspectOptions) (client.VolumeInspectResult, error)
	VolumeCreate(context.Context, client.VolumeCreateOptions) (client.VolumeCreateResult, error)
}, names []string, appKey, applicationID string) error {
	for _, name := range names {
		inspected, err := docker.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("agent: inspect Docker volume %s: %w", name, err)
		}
		if validateApplicationResourceLabels(inspected.Volume.Labels, appKey, applicationVolumeComponent(name), applicationID, "") != nil {
			return fmt.Errorf("agent: refusing to use unowned Docker volume %s", name)
		}
	}
	for _, name := range names {
		if err := ensureOwnedApplicationVolume(ctx, docker, name, appKey, applicationVolumeComponent(name), applicationID); err != nil {
			return err
		}
	}
	return nil
}

func inspectOwnedApplicationVolume(ctx context.Context, docker interface {
	VolumeInspect(context.Context, string, client.VolumeInspectOptions) (client.VolumeInspectResult, error)
}, name, appKey, component, applicationID string) (client.VolumeInspectResult, bool, error) {
	inspected, err := docker.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if errdefs.IsNotFound(err) {
		return client.VolumeInspectResult{}, false, nil
	}
	if err != nil {
		return client.VolumeInspectResult{}, false, fmt.Errorf("agent: inspect Docker volume %s: %w", name, err)
	}
	if validateApplicationResourceLabels(inspected.Volume.Labels, appKey, component, applicationID, "") != nil {
		return client.VolumeInspectResult{}, false, fmt.Errorf("agent: refusing to use unowned Docker volume %s", name)
	}
	return inspected, true, nil
}

func removeOwnedApplicationVolume(ctx context.Context, docker interface {
	VolumeInspect(context.Context, string, client.VolumeInspectOptions) (client.VolumeInspectResult, error)
	VolumeRemove(context.Context, string, client.VolumeRemoveOptions) (client.VolumeRemoveResult, error)
}, name, appKey, component, applicationID string) error {
	inspected, exists, err := inspectOwnedApplicationVolume(ctx, docker, name, appKey, component, applicationID)
	if err != nil || !exists {
		return err
	}
	if _, err := docker.VolumeRemove(ctx, inspected.Volume.Name, client.VolumeRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		if _, stillExists, inspectErr := inspectOwnedApplicationVolume(ctx, docker, name, appKey, component, applicationID); inspectErr == nil && !stillExists {
			return nil
		}
		return fmt.Errorf("agent: remove owned Docker volume %s: %w", name, err)
	}
	return nil
}

func applicationVolumeComponent(name string) string {
	return "application-volume:" + name
}
