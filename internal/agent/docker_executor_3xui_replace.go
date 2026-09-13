package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

const (
	threeXUICandidateContainer = threeXUIContainer + "-candidate"
	threeXUIBackupContainer    = threeXUIContainer + "-rollback"
	threeXUICleanupContainer   = threeXUIContainer + "-cleanup"
	threeXUIDurableSnapshot    = "/.vastora-3x-ui-rollback"
	threeXUIVolumeStateLabel   = "io.vastora.3x-ui.database-state"
	threeXUIDeploymentIDLabel  = applicationDeploymentIDLabel
)

var (
	errThreeXUIDatabaseMissing = errors.New("3x-ui database snapshot does not contain x-ui.db")
	errThreeXUIVolumeEmpty     = errors.New("3x-ui database volume is empty")
)

type threeXUIContainerEngine interface {
	ContainerCreate(context.Context, client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerStop(context.Context, string, client.ContainerStopOptions) (client.ContainerStopResult, error)
	ContainerRemove(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	ContainerRename(context.Context, string, client.ContainerRenameOptions) (client.ContainerRenameResult, error)
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	CopyFromContainer(context.Context, string, client.CopyFromContainerOptions) (client.CopyFromContainerResult, error)
	CopyToContainer(context.Context, string, client.CopyToContainerOptions) (client.CopyToContainerResult, error)
	VolumeCreate(context.Context, client.VolumeCreateOptions) (client.VolumeCreateResult, error)
	VolumeInspect(context.Context, string, client.VolumeInspectOptions) (client.VolumeInspectResult, error)
	VolumeRemove(context.Context, string, client.VolumeRemoveOptions) (client.VolumeRemoveResult, error)
}

func replaceThreeXUIContainer(ctx context.Context, docker threeXUIContainerEngine, createOptions client.ContainerCreateOptions, allowFreshState bool, validate func(string) (string, error), verifyPromotion func(string, string) error) (string, error) {
	if validate == nil || verifyPromotion == nil {
		return "", errors.New("agent: 3x-ui replacement validation is missing")
	}
	if createOptions.Config == nil || validateApplicationResourceLabels(createOptions.Config.Labels, threeXUIKey, "3x-ui", "", anyApplicationDeployment) != nil {
		return "", errors.New("agent: 3x-ui candidate ownership is missing")
	}
	applicationID := createOptions.Config.Labels[applicationInstallationLabel]
	if err := validateThreeXUIOwnership(ctx, docker, applicationID); err != nil {
		return "", err
	}
	if err := requireNoInterruptedThreeXUIDeploy(ctx, docker); err != nil {
		return "", err
	}
	previous, previousExists, err := inspectThreeXUIContainer(ctx, docker, threeXUIContainer)
	if err != nil {
		return "", fmt.Errorf("agent: inspect current 3x-ui container: %w", err)
	}
	if previousExists && previous.Container.Config.Labels[applicationInstallationLabel] != applicationID {
		return "", errors.New("agent: refusing to replace a 3x-ui container owned by another application")
	}
	_, databaseVolumeExists, err := inspectOwnedApplicationVolume(ctx, docker, threeXUIDatabaseVolume, threeXUIKey, applicationVolumeComponent(threeXUIDatabaseVolume), applicationID)
	if err != nil {
		return "", fmt.Errorf("agent: inspect retained 3x-ui database volume: %w", err)
	}
	databaseVolumeFresh := !databaseVolumeExists
	if databaseVolumeFresh {
		if !allowFreshState {
			return "", errors.New("agent: this change requires an existing 3x-ui database")
		}
		if err := ensureOwnedApplicationVolume(ctx, docker, threeXUIDatabaseVolume, threeXUIKey, applicationVolumeComponent(threeXUIDatabaseVolume), applicationID); err != nil {
			return "", fmt.Errorf("agent: create 3x-ui database volume: %w", err)
		}
		_, databaseVolumeExists, err = inspectOwnedApplicationVolume(ctx, docker, threeXUIDatabaseVolume, threeXUIKey, applicationVolumeComponent(threeXUIDatabaseVolume), applicationID)
		if err != nil {
			return "", fmt.Errorf("agent: verify created 3x-ui database volume: %w", err)
		}
		if !databaseVolumeExists {
			return "", errors.New("agent: created 3x-ui database volume is missing")
		}
	}
	if createOptions.Config.Labels == nil {
		createOptions.Config.Labels = map[string]string{}
	}
	if !databaseVolumeFresh {
		createOptions.Config.Labels[threeXUIVolumeStateLabel] = "retained"
	} else {
		createOptions.Config.Labels[threeXUIVolumeStateLabel] = "fresh"
	}
	created, err := docker.ContainerCreate(ctx, createOptions)
	if err != nil {
		return "", fmt.Errorf("agent: create 3x-ui candidate: %w", err)
	}
	candidateID := created.ID
	previousRunning := previousExists && previous.Container.State != nil && previous.Container.State.Running
	if previousRunning {
		timeout := 10
		if _, err := docker.ContainerStop(ctx, previous.Container.ID, client.ContainerStopOptions{Timeout: &timeout}); err != nil {
			return "", uncertainTaskOutcome(fmt.Errorf("agent: stop current 3x-ui container: %w", err))
		}
	}
	var databaseSnapshot []byte
	snapshotContainerID := ""
	if previousExists {
		snapshotContainerID = previous.Container.ID
	} else if !databaseVolumeFresh {
		snapshotContainerID = candidateID
	}
	if snapshotContainerID != "" {
		databaseSnapshot, err = snapshotThreeXUIDatabase(ctx, docker, snapshotContainerID)
		if err != nil {
			return "", uncertainTaskOutcome(fmt.Errorf("agent: snapshot current 3x-ui database: %w", err))
		}
		if len(databaseSnapshot) != 0 {
			if err := persistThreeXUIDatabaseSnapshot(ctx, docker, snapshotContainerID, databaseSnapshot); err != nil {
				return "", uncertainTaskOutcome(fmt.Errorf("agent: persist 3x-ui rollback database: %w", err))
			}
		}
	}
	if previousExists {
		if _, err := docker.ContainerRename(ctx, previous.Container.ID, client.ContainerRenameOptions{NewName: threeXUIBackupContainer}); err != nil {
			return "", uncertainTaskOutcome(fmt.Errorf("agent: preserve previous 3x-ui container: %w", err))
		}
	}
	if _, err := docker.ContainerStart(ctx, candidateID, client.ContainerStartOptions{}); err != nil {
		return "", uncertainTaskOutcome(fmt.Errorf("agent: start 3x-ui candidate: %w", err))
	}
	result, err := validate(candidateID)
	if err != nil {
		return result, uncertainTaskOutcome(err)
	}
	inspected, err := docker.ContainerInspect(ctx, candidateID, client.ContainerInspectOptions{})
	if err != nil {
		return result, uncertainTaskOutcome(fmt.Errorf("agent: inspect 3x-ui candidate: %w", err))
	}
	if inspected.Container.State == nil || !inspected.Container.State.Running {
		return result, uncertainTaskOutcome(errors.New("agent: 3x-ui candidate did not remain running"))
	}
	if _, err := docker.ContainerRename(ctx, candidateID, client.ContainerRenameOptions{NewName: threeXUIContainer}); err != nil {
		return result, uncertainTaskOutcome(fmt.Errorf("agent: promote 3x-ui candidate: %w", err))
	}
	if err := verifyPromotion(candidateID, result); err != nil {
		return result, uncertainTaskOutcome(fmt.Errorf("agent: verify promoted 3x-ui container: %w", err))
	}
	if previousExists {
		if _, err := docker.ContainerRename(ctx, previous.Container.ID, client.ContainerRenameOptions{NewName: threeXUICleanupContainer}); err != nil {
			return result, uncertainTaskOutcome(fmt.Errorf("agent: commit promoted 3x-ui container: %w", err))
		}
		if _, err := docker.ContainerRemove(ctx, previous.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return result, uncertainTaskOutcome(fmt.Errorf("agent: remove retained 3x-ui container: %w", err))
		}
	}
	return result, nil
}

func inspectThreeXUIVolume(ctx context.Context, docker threeXUIContainerEngine, name string) (client.VolumeInspectResult, bool, error) {
	return inspectOwnedApplicationVolume(ctx, docker, name, threeXUIKey, applicationVolumeComponent(name), "")
}

func validateThreeXUIOwnership(ctx context.Context, docker threeXUIContainerEngine, expectedApplicationID string) error {
	applicationID := ""
	for _, name := range []string{threeXUIContainer, threeXUICandidateContainer, threeXUIBackupContainer, threeXUICleanupContainer} {
		inspected, exists, err := inspectThreeXUIContainer(ctx, docker, name)
		if err != nil {
			return fmt.Errorf("agent: verify 3x-ui container ownership: %w", err)
		}
		if !exists {
			continue
		}
		value := inspected.Container.Config.Labels[applicationInstallationLabel]
		if applicationID == "" {
			applicationID = value
		} else if applicationID != value {
			return errors.New("agent: conflicting 3x-ui application ownership markers")
		}
	}
	database, exists, err := inspectThreeXUIVolume(ctx, docker, threeXUIDatabaseVolume)
	if err != nil {
		return fmt.Errorf("agent: verify 3x-ui database ownership: %w", err)
	}
	if exists {
		value := database.Volume.Labels[applicationInstallationLabel]
		if applicationID == "" {
			applicationID = value
		} else if applicationID != value {
			return errors.New("agent: 3x-ui database belongs to another application")
		}
	}
	if expectedApplicationID != "" && applicationID != "" && applicationID != expectedApplicationID {
		return errors.New("agent: refusing to mutate 3x-ui resources owned by another application")
	}
	return nil
}

func requireNoInterruptedThreeXUIDeploy(ctx context.Context, docker threeXUIContainerEngine) error {
	for _, name := range []string{threeXUICandidateContainer, threeXUIBackupContainer, threeXUICleanupContainer} {
		if _, exists, err := inspectThreeXUIContainer(ctx, docker, name); err != nil {
			return err
		} else if exists {
			return uncertainTaskOutcome(fmt.Errorf("agent: retained 3x-ui replacement %s requires explicit review", name))
		}
	}
	return nil
}

// Keep-data uninstall stops writers but never restores a snapshot or starts a
// service. The shared volume and durable snapshot remain untouched.
func prepareThreeXUIKeepDataUninstall(ctx context.Context, docker threeXUIContainerEngine) error {
	for _, name := range []string{threeXUICandidateContainer, threeXUIBackupContainer, threeXUICleanupContainer, threeXUIContainer} {
		container, exists, err := inspectThreeXUIContainer(ctx, docker, name)
		if err != nil {
			return err
		}
		if !exists || container.Container.State == nil || !container.Container.State.Running {
			continue
		}
		if _, err := docker.ContainerStop(ctx, container.Container.ID, client.ContainerStopOptions{}); err != nil && !errdefs.IsNotModified(err) && !errdefs.IsNotFound(err) {
			return uncertainTaskOutcome(fmt.Errorf("agent: stop 3x-ui container before preserving data: %w", err))
		}
	}
	return nil
}

func inspectThreeXUIContainer(ctx context.Context, docker threeXUIContainerEngine, name string) (client.ContainerInspectResult, bool, error) {
	return inspectOwnedApplicationContainer(ctx, docker, name, threeXUIKey, "3x-ui", "", anyApplicationDeployment)
}

func removeThreeXUIContainerIfExists(ctx context.Context, docker threeXUIContainerEngine, name string) error {
	return removeOwnedApplicationContainer(ctx, docker, name, threeXUIKey, "3x-ui", "", anyApplicationDeployment)
}
