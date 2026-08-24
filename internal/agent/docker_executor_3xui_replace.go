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
	threeXUIVolumeOwnerLabel   = "io.vastora.3x-ui.database-volume"
	threeXUIDeploymentIDLabel  = "io.vastora.3x-ui.deployment-id"
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

func replaceThreeXUIContainer(ctx context.Context, docker threeXUIContainerEngine, createOptions client.ContainerCreateOptions, validate func(string) (string, error), verifyPromotion func(string, string) error) (string, error) {
	if validate == nil || verifyPromotion == nil {
		return "", errors.New("agent: 3x-ui replacement validation is missing")
	}
	if err := recoverInterruptedThreeXUIDeploy(ctx, docker); err != nil {
		return "", err
	}
	previous, previousExists, err := inspectThreeXUIContainer(ctx, docker, threeXUIContainer)
	if err != nil {
		return "", fmt.Errorf("agent: inspect current 3x-ui container: %w", err)
	}
	if err := removeThreeXUIContainerIfExists(ctx, docker, threeXUICandidateContainer); err != nil {
		return "", fmt.Errorf("agent: clear stale 3x-ui candidate: %w", err)
	}
	databaseVolume, databaseVolumeExists, err := inspectThreeXUIVolume(ctx, docker, threeXUIDatabaseVolume)
	if err != nil {
		return "", fmt.Errorf("agent: inspect retained 3x-ui database volume: %w", err)
	}
	databaseVolumeFresh := !databaseVolumeExists
	if databaseVolumeFresh {
		createdVolume, createErr := docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: threeXUIDatabaseVolume, Labels: map[string]string{threeXUIVolumeOwnerLabel: "true"}})
		err = createErr
		if err != nil {
			return "", fmt.Errorf("agent: create 3x-ui database volume: %w", err)
		}
		databaseVolume = client.VolumeInspectResult{Volume: createdVolume.Volume}
		databaseVolumeFresh = createdVolume.Volume.Labels[threeXUIVolumeOwnerLabel] == "true"
	}
	if createOptions.Config == nil {
		return "", errors.New("agent: 3x-ui candidate configuration is missing")
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
	abortBeforeRename := func(operationErr error) error {
		failures := []error{operationErr}
		if previousRunning {
			if restartErr := restartThreeXUIContainerIfNeeded(ctx, docker, previous.Container.ID, true); restartErr != nil {
				failures = append(failures, fmt.Errorf("agent: restore current 3x-ui container: %w", restartErr))
				// Keep the candidate as a durable interruption marker. Maintenance
				// will retry the restart before removing that marker.
				return deferTaskCompletion(errors.Join(failures...))
			}
		}
		if cleanupErr := removeThreeXUIContainerIfExists(ctx, docker, candidateID); cleanupErr != nil {
			failures = append(failures, fmt.Errorf("agent: remove incomplete 3x-ui candidate: %w", cleanupErr))
			return deferTaskCompletion(errors.Join(failures...))
		}
		return errors.Join(failures...)
	}
	if previousRunning {
		timeout := 10
		if _, err := docker.ContainerStop(ctx, previous.Container.ID, client.ContainerStopOptions{Timeout: &timeout}); err != nil {
			return "", abortBeforeRename(fmt.Errorf("agent: stop current 3x-ui container: %w", err))
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
			// A volume explicitly created by a prior interrupted Vastora install is
			// safe to recycle only when it contains no files at all. An unlabelled
			// or partially populated keep-data volume fails closed.
			managedEmptyVolume := !previousExists && errors.Is(err, errThreeXUIVolumeEmpty) && databaseVolume.Volume.Labels[threeXUIVolumeOwnerLabel] == "true"
			if managedEmptyVolume {
				if cleanupErr := removeThreeXUIContainerIfExists(ctx, docker, candidateID); cleanupErr != nil {
					return "", errors.Join(fmt.Errorf("agent: clear empty 3x-ui database candidate: %w", err), cleanupErr)
				}
				if _, volumeErr := docker.VolumeRemove(ctx, threeXUIDatabaseVolume, client.VolumeRemoveOptions{Force: true}); volumeErr != nil && !errdefs.IsNotFound(volumeErr) {
					createOptions.Config.Labels[threeXUIVolumeStateLabel] = "fresh"
					_, markerErr := recreateThreeXUICandidateMarker(ctx, docker, createOptions)
					return "", deferTaskCompletion(errors.Join(fmt.Errorf("agent: clear empty 3x-ui database volume: %w", err), volumeErr, markerErr))
				}
				databaseVolumeFresh = true
				if _, createVolumeErr := docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: threeXUIDatabaseVolume, Labels: map[string]string{threeXUIVolumeOwnerLabel: "true"}}); createVolumeErr != nil {
					return "", fmt.Errorf("agent: recreate empty 3x-ui database volume: %w", createVolumeErr)
				}
				createOptions.Config.Labels[threeXUIVolumeStateLabel] = "fresh"
				recreated, createErr := docker.ContainerCreate(ctx, createOptions)
				if createErr != nil {
					return "", fmt.Errorf("agent: recreate 3x-ui candidate with a fresh database: %w", createErr)
				}
				candidateID = recreated.ID
				databaseSnapshot = nil
				err = nil
			} else {
				return "", abortBeforeRename(fmt.Errorf("agent: snapshot current 3x-ui database: %w", err))
			}
		}
		if len(databaseSnapshot) != 0 {
			if err := persistThreeXUIDatabaseSnapshot(ctx, docker, snapshotContainerID, databaseSnapshot); err != nil {
				return "", abortBeforeRename(fmt.Errorf("agent: persist 3x-ui rollback database: %w", err))
			}
		}
	}
	rollback := func(operationErr error) error {
		failures := []error{operationErr}
		if _, stopErr := docker.ContainerStop(ctx, candidateID, client.ContainerStopOptions{}); stopErr != nil && !errdefs.IsNotModified(stopErr) && !errdefs.IsNotFound(stopErr) {
			failures = append(failures, fmt.Errorf("agent: stop failed 3x-ui candidate: %w", stopErr))
			// A failed Stop response is never proof that an unless-stopped
			// container cannot restart after an Inspect. Never overwrite the shared
			// SQLite volume until a later Stop succeeds unequivocally.
			return deferTaskCompletion(errors.Join(failures...))
		}
		rollbackReady := true
		retryRequired := false
		if len(databaseSnapshot) != 0 {
			restoreContainerID := candidateID
			if previousExists {
				restoreContainerID = previous.Container.ID
			}
			if restoreErr := restoreThreeXUIDatabase(ctx, docker, restoreContainerID, databaseSnapshot); restoreErr != nil {
				rollbackReady = false
				retryRequired = true
				failures = append(failures, fmt.Errorf("agent: restore previous 3x-ui database: %w", restoreErr))
			}
		}
		if previousExists && rollbackReady {
			if _, restored, inspectErr := inspectThreeXUIContainer(ctx, docker, threeXUIContainer); inspectErr != nil {
				rollbackReady = false
				retryRequired = true
				failures = append(failures, fmt.Errorf("agent: inspect 3x-ui rollback target: %w", inspectErr))
			} else if !restored {
				if _, renameErr := docker.ContainerRename(ctx, previous.Container.ID, client.ContainerRenameOptions{NewName: threeXUIContainer}); renameErr != nil {
					rollbackReady = false
					retryRequired = true
					failures = append(failures, fmt.Errorf("agent: restore 3x-ui container name: %w", renameErr))
				}
			}
			if previousRunning && rollbackReady {
				if startErr := restartThreeXUIContainerIfNeeded(ctx, docker, previous.Container.ID, true); startErr != nil {
					rollbackReady = false
					retryRequired = true
					failures = append(failures, fmt.Errorf("agent: restart previous 3x-ui container: %w", startErr))
				}
			}
		}
		// Keep the candidate as an interruption marker until the shared database
		// and the prior service are fully restored. A later task retry can then
		// finish recovery after an Agent or Docker crash at any point above.
		candidateRemoved := false
		if rollbackReady {
			if err := removeThreeXUIContainerIfExists(ctx, docker, candidateID); err != nil {
				retryRequired = true
				failures = append(failures, fmt.Errorf("agent: remove failed 3x-ui candidate: %w", err))
			} else {
				candidateRemoved = true
			}
		}
		if rollbackReady && candidateRemoved && databaseVolumeFresh {
			if _, err := docker.VolumeRemove(ctx, threeXUIDatabaseVolume, client.VolumeRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
				retryRequired = true
				failures = append(failures, fmt.Errorf("agent: remove incomplete fresh 3x-ui database volume: %w", err))
				_, markerErr := recreateThreeXUICandidateMarker(ctx, docker, createOptions)
				if markerErr != nil {
					failures = append(failures, fmt.Errorf("agent: retain fresh 3x-ui cleanup marker: %w", markerErr))
				}
			}
		}
		resultErr := errors.Join(failures...)
		if retryRequired {
			return deferTaskCompletion(resultErr)
		}
		return resultErr
	}
	if previousExists {
		if _, err := docker.ContainerRename(ctx, previous.Container.ID, client.ContainerRenameOptions{NewName: threeXUIBackupContainer}); err != nil {
			return "", rollback(fmt.Errorf("agent: preserve previous 3x-ui container: %w", err))
		}
	}
	if _, err := docker.ContainerStart(ctx, candidateID, client.ContainerStartOptions{}); err != nil {
		return "", rollback(fmt.Errorf("agent: start 3x-ui candidate: %w", err))
	}
	result, err := validate(candidateID)
	if err != nil {
		return "", rollback(err)
	}
	inspected, err := docker.ContainerInspect(ctx, candidateID, client.ContainerInspectOptions{})
	if err != nil {
		return "", rollback(fmt.Errorf("agent: inspect 3x-ui candidate: %w", err))
	}
	if inspected.Container.State == nil || !inspected.Container.State.Running {
		return "", rollback(errors.New("agent: 3x-ui candidate did not remain running"))
	}
	if _, err := docker.ContainerRename(ctx, candidateID, client.ContainerRenameOptions{NewName: threeXUIContainer}); err != nil {
		// Rename is not idempotent and Docker can lose the response after the
		// server committed it. Verify by immutable container ID before deciding
		// whether rollback is still safe.
		promoted, promotedExists, inspectErr := inspectThreeXUIContainer(ctx, docker, threeXUIContainer)
		if inspectErr != nil {
			return "", deferTaskUntilReconciled(errors.Join(fmt.Errorf("agent: promote 3x-ui candidate: %w", err), fmt.Errorf("agent: verify promoted 3x-ui candidate: %w", inspectErr)))
		}
		if !promotedExists || promoted.Container.ID != candidateID {
			return "", rollback(fmt.Errorf("agent: promote 3x-ui candidate: %w", err))
		}
	}
	if err := verifyPromotion(candidateID, result); err != nil {
		if !previousExists {
			return "", rollback(fmt.Errorf("agent: verify promoted 3x-ui container: %w", err))
		}
		rollbackErr := recoverInterruptedThreeXUIDeploy(ctx, docker)
		if rollbackErr != nil {
			return "", deferTaskUntilReconciled(errors.Join(fmt.Errorf("agent: verify promoted 3x-ui container: %w", err), rollbackErr))
		}
		return "", fmt.Errorf("agent: verify promoted 3x-ui container: %w", err)
	}
	if previousExists {
		if _, err := docker.ContainerRename(ctx, previous.Container.ID, client.ContainerRenameOptions{NewName: threeXUICleanupContainer}); err != nil {
			cleanup, cleanupExists, inspectErr := inspectThreeXUIContainer(ctx, docker, threeXUICleanupContainer)
			if inspectErr != nil {
				return "", deferTaskUntilReconciled(errors.Join(fmt.Errorf("agent: commit promoted 3x-ui container: %w", err), fmt.Errorf("agent: verify 3x-ui cleanup marker: %w", inspectErr)))
			}
			if !cleanupExists || cleanup.Container.ID != previous.Container.ID {
				// The old rollback name remains an uncommitted marker. Recovery will
				// restore it instead of treating this promotion as successful.
				return "", deferTaskUntilReconciled(fmt.Errorf("agent: commit promoted 3x-ui container: %w", err))
			}
		}
		if _, err := docker.ContainerRemove(ctx, previous.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			// Promotion is already committed and the API token belongs to the live
			// current container. The cleanup name is a durable commit marker, so
			// maintenance can remove it without ever rolling the promotion back.
			return result, nil
		}
	}
	return result, nil
}

func restartThreeXUIContainerIfNeeded(ctx context.Context, docker threeXUIContainerEngine, containerID string, shouldStart bool) error {
	if !shouldStart {
		return nil
	}
	_, err := docker.ContainerStart(ctx, containerID, client.ContainerStartOptions{})
	if errdefs.IsNotModified(err) {
		return nil
	}
	return err
}

func inspectThreeXUIVolume(ctx context.Context, docker threeXUIContainerEngine, name string) (client.VolumeInspectResult, bool, error) {
	result, err := docker.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
	if errdefs.IsNotFound(err) {
		return client.VolumeInspectResult{}, false, nil
	}
	return result, err == nil, err
}

func recoverInterruptedThreeXUIDeploy(ctx context.Context, docker threeXUIContainerEngine) error {
	current, currentExists, err := inspectThreeXUIContainer(ctx, docker, threeXUIContainer)
	if err != nil {
		return fmt.Errorf("agent: inspect 3x-ui deployment state: %w", err)
	}
	candidate, candidateExists, err := inspectThreeXUIContainer(ctx, docker, threeXUICandidateContainer)
	if err != nil {
		return fmt.Errorf("agent: inspect interrupted 3x-ui candidate: %w", err)
	}
	backup, backupExists, err := inspectThreeXUIContainer(ctx, docker, threeXUIBackupContainer)
	if err != nil {
		return fmt.Errorf("agent: inspect 3x-ui rollback state: %w", err)
	}
	cleanup, cleanupExists, err := inspectThreeXUIContainer(ctx, docker, threeXUICleanupContainer)
	if err != nil {
		return fmt.Errorf("agent: inspect committed 3x-ui cleanup state: %w", err)
	}
	if backupExists && cleanupExists {
		return errors.New("agent: conflicting 3x-ui rollback and cleanup markers")
	}
	if currentExists {
		// The rollback name is deliberately retained until the promoted canonical
		// container passes its post-rename health check. Its presence therefore
		// always means promotion was not committed and must be rolled back.
		if backupExists {
			return restoreInterruptedThreeXUIDeploy(ctx, docker, current, true, backup)
		}
		// A candidate marker proves a pre-promotion replacement stopped the old
		// current container and still needs to put it back.
		if candidateExists && (current.Container.State == nil || !current.Container.State.Running) {
			if err := restartThreeXUIContainerIfNeeded(ctx, docker, current.Container.ID, true); err != nil {
				return fmt.Errorf("agent: restart interrupted previous 3x-ui deployment: %w", err)
			}
		}
		if candidateExists {
			if _, err := docker.ContainerRemove(ctx, candidate.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
				return fmt.Errorf("agent: remove interrupted 3x-ui candidate: %w", err)
			}
		}
		if cleanupExists {
			if _, err := docker.ContainerRemove(ctx, cleanup.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
				return fmt.Errorf("agent: remove committed 3x-ui cleanup container: %w", err)
			}
		}
		return nil
	}
	if backupExists {
		return restoreInterruptedThreeXUIDeploy(ctx, docker, current, currentExists, backup)
	}
	if cleanupExists {
		// Losing the promoted canonical container before its committed cleanup
		// marker is removed leaves the old container as the only recoverable copy.
		return restoreInterruptedThreeXUIDeploy(ctx, docker, current, currentExists, cleanup)
	}
	if candidateExists {
		// With no canonical or rollback container, a durable snapshot on the
		// candidate identifies a keep-data reinstall. Restore the named volume
		// before removing the only container that carries its rollback copy.
		volumeState := ""
		if candidate.Container.Config != nil {
			volumeState = candidate.Container.Config.Labels[threeXUIVolumeStateLabel]
		}
		snapshot, snapshotErr := loadDurableThreeXUIDatabaseSnapshot(ctx, docker, candidate.Container.ID)
		if snapshotErr == nil {
			if _, err := docker.ContainerStop(ctx, candidate.Container.ID, client.ContainerStopOptions{}); err != nil && !errdefs.IsNotModified(err) && !errdefs.IsNotFound(err) {
				return fmt.Errorf("agent: stop interrupted retained-data 3x-ui candidate: %w", err)
			}
			if err := restoreThreeXUIDatabase(ctx, docker, candidate.Container.ID, snapshot); err != nil {
				return fmt.Errorf("agent: restore interrupted retained 3x-ui database: %w", err)
			}
		} else if !errdefs.IsNotFound(snapshotErr) {
			return fmt.Errorf("agent: load interrupted retained 3x-ui database rollback: %w", snapshotErr)
		}
		if _, err := docker.ContainerRemove(ctx, candidate.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("agent: remove interrupted 3x-ui candidate: %w", err)
		}
		if snapshotErr != nil && volumeState == "fresh" {
			if _, err := docker.VolumeRemove(ctx, threeXUIDatabaseVolume, client.VolumeRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
				markerOptions, markerOptionsErr := candidateMarkerOptions(candidate)
				if markerOptionsErr != nil {
					return errors.Join(fmt.Errorf("agent: remove interrupted fresh 3x-ui database volume: %w", err), markerOptionsErr)
				}
				_, markerErr := recreateThreeXUICandidateMarker(ctx, docker, markerOptions)
				return errors.Join(fmt.Errorf("agent: remove interrupted fresh 3x-ui database volume: %w", err), markerErr)
			}
		}
	}
	return nil
}

func prepareThreeXUIKeepDataUninstall(ctx context.Context, docker threeXUIContainerEngine) error {
	current, currentExists, err := inspectThreeXUIContainer(ctx, docker, threeXUIContainer)
	if err != nil {
		return fmt.Errorf("agent: inspect current 3x-ui container: %w", err)
	}
	if currentExists {
		candidate, candidateExists, err := inspectThreeXUIContainer(ctx, docker, threeXUICandidateContainer)
		if err != nil {
			return fmt.Errorf("agent: inspect 3x-ui candidate before uninstall: %w", err)
		}
		backup, backupExists, err := inspectThreeXUIContainer(ctx, docker, threeXUIBackupContainer)
		if err != nil {
			return fmt.Errorf("agent: inspect 3x-ui rollback before uninstall: %w", err)
		}
		cleanup, cleanupExists, err := inspectThreeXUIContainer(ctx, docker, threeXUICleanupContainer)
		if err != nil {
			return fmt.Errorf("agent: inspect 3x-ui cleanup before uninstall: %w", err)
		}
		// A running rollback beside a stopped canonical container is the one
		// legacy state where the canonical name is not authoritative yet. Finish
		// that rollback before choosing which container owns the retained data.
		if backupExists && current.Container.State != nil && !current.Container.State.Running && backup.Container.State != nil && backup.Container.State.Running {
			if err := recoverInterruptedThreeXUIDeploy(ctx, docker); err != nil {
				return fmt.Errorf("agent: recover 3x-ui rollback before uninstall: %w", err)
			}
			return prepareThreeXUIKeepDataUninstall(ctx, docker)
		}
		// Remove every recovery marker before stopping the authoritative
		// container. If the Agent exits after the Stop, maintenance then sees only
		// a lone stopped current and cannot mistake the uninstall for an
		// interrupted replacement that should be restarted.
		if candidateExists {
			if _, err := docker.ContainerRemove(ctx, candidate.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
				return fmt.Errorf("agent: remove stale 3x-ui candidate before uninstall: %w", err)
			}
		}
		if backupExists {
			if _, err := docker.ContainerRemove(ctx, backup.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
				return fmt.Errorf("agent: remove stale 3x-ui rollback before uninstall: %w", err)
			}
		}
		if cleanupExists {
			if _, err := docker.ContainerRemove(ctx, cleanup.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
				return fmt.Errorf("agent: remove committed 3x-ui cleanup before uninstall: %w", err)
			}
		}
		return stopThreeXUIContainerForDataPreservation(ctx, docker, current)
	}
	candidate, candidateExists, err := inspectThreeXUIContainer(ctx, docker, threeXUICandidateContainer)
	if err != nil {
		return fmt.Errorf("agent: inspect 3x-ui candidate before uninstall: %w", err)
	}
	_, backupExists, err := inspectThreeXUIContainer(ctx, docker, threeXUIBackupContainer)
	if err != nil {
		return fmt.Errorf("agent: inspect 3x-ui rollback before uninstall: %w", err)
	}
	_, cleanupExists, err := inspectThreeXUIContainer(ctx, docker, threeXUICleanupContainer)
	if err != nil {
		return fmt.Errorf("agent: inspect 3x-ui cleanup before uninstall: %w", err)
	}
	if backupExists || cleanupExists {
		// Normalize the interrupted replacement first. Recovery may briefly start
		// the restored canonical service, but no uninstall Stop has committed yet.
		// The recursive current-container path removes every marker before its
		// final Stop, so a crash can never make maintenance reverse that Stop.
		if err := recoverInterruptedThreeXUIDeploy(ctx, docker); err != nil {
			return fmt.Errorf("agent: recover retained 3x-ui rollback before uninstall: %w", err)
		}
		return prepareThreeXUIKeepDataUninstall(ctx, docker)
	}
	if !candidateExists {
		return nil
	}
	snapshot, snapshotErr := loadDurableThreeXUIDatabaseSnapshot(ctx, docker, candidate.Container.ID)
	if snapshotErr != nil && !errdefs.IsNotFound(snapshotErr) {
		return fmt.Errorf("agent: load retained 3x-ui candidate rollback before uninstall: %w", snapshotErr)
	}
	if snapshotErr == nil {
		if err := stopThreeXUIContainerForDataPreservation(ctx, docker, candidate); err != nil {
			return err
		}
		if err := restoreThreeXUIDatabase(ctx, docker, candidate.Container.ID, snapshot); err != nil {
			return fmt.Errorf("agent: restore retained 3x-ui candidate database before uninstall: %w", err)
		}
		if _, err := docker.ContainerRemove(ctx, candidate.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("agent: remove restored 3x-ui candidate before uninstall: %w", err)
		}
		return nil
	}
	// With no durable rollback snapshot the candidate's live database is the
	// only data to retain. Promote its name before stopping it: a crash before
	// the Stop leaves a running canonical service, while a crash after the Stop
	// leaves a lone stopped canonical service that maintenance never restarts.
	if _, err := docker.ContainerRename(ctx, candidate.Container.ID, client.ContainerRenameOptions{NewName: threeXUIContainer}); err != nil {
		promoted, promotedExists, inspectErr := inspectThreeXUIContainer(ctx, docker, threeXUIContainer)
		if inspectErr != nil {
			return deferTaskUntilReconciled(errors.Join(fmt.Errorf("agent: retain 3x-ui candidate before uninstall: %w", err), fmt.Errorf("agent: verify retained 3x-ui candidate: %w", inspectErr)))
		}
		if !promotedExists || promoted.Container.ID != candidate.Container.ID {
			return fmt.Errorf("agent: retain 3x-ui candidate before uninstall: %w", err)
		}
	}
	return prepareThreeXUIKeepDataUninstall(ctx, docker)
}

func stopThreeXUIContainerForDataPreservation(ctx context.Context, docker threeXUIContainerEngine, inspected client.ContainerInspectResult) error {
	if inspected.Container.State == nil || !inspected.Container.State.Running {
		return nil
	}
	if _, err := docker.ContainerStop(ctx, inspected.Container.ID, client.ContainerStopOptions{}); err != nil && !errdefs.IsNotModified(err) && !errdefs.IsNotFound(err) {
		// Docker can commit the Stop and lose the response. The immutable ID lets
		// this uninstall distinguish that success from a container which may still
		// restart under its unless-stopped policy.
		current, inspectErr := docker.ContainerInspect(ctx, inspected.Container.ID, client.ContainerInspectOptions{})
		if inspectErr == nil && current.Container.State != nil && !current.Container.State.Running {
			return nil
		}
		return deferTaskUntilReconciled(errors.Join(fmt.Errorf("agent: stop 3x-ui container before preserving data: %w", err), inspectErr))
	}
	return nil
}

func candidateMarkerOptions(candidate client.ContainerInspectResult) (client.ContainerCreateOptions, error) {
	if candidate.Container.Config == nil || candidate.Container.HostConfig == nil {
		return client.ContainerCreateOptions{}, errors.New("agent: interrupted 3x-ui candidate cleanup marker is incomplete")
	}
	return client.ContainerCreateOptions{Name: threeXUICandidateContainer, Config: candidate.Container.Config, HostConfig: candidate.Container.HostConfig}, nil
}

func recreateThreeXUICandidateMarker(ctx context.Context, docker threeXUIContainerEngine, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	options.Name = threeXUICandidateContainer
	if options.Config == nil {
		return client.ContainerCreateResult{}, errors.New("agent: 3x-ui cleanup marker configuration is missing")
	}
	if options.Config.Labels == nil {
		options.Config.Labels = map[string]string{}
	}
	options.Config.Labels[threeXUIVolumeStateLabel] = "fresh"
	created, err := docker.ContainerCreate(ctx, options)
	if err != nil {
		return client.ContainerCreateResult{}, fmt.Errorf("agent: recreate 3x-ui cleanup marker: %w", err)
	}
	return created, nil
}

func maintainThreeXUIContainers(ctx context.Context, docker threeXUIContainerEngine) error {
	_, currentExists, err := inspectThreeXUIContainer(ctx, docker, threeXUIContainer)
	if err != nil {
		return fmt.Errorf("agent: inspect committed 3x-ui deployment during maintenance: %w", err)
	}
	if !currentExists {
		_, candidateExists, candidateErr := inspectThreeXUIContainer(ctx, docker, threeXUICandidateContainer)
		if candidateErr != nil {
			return fmt.Errorf("agent: inspect interrupted 3x-ui candidate during maintenance: %w", candidateErr)
		}
		_, backupExists, backupErr := inspectThreeXUIContainer(ctx, docker, threeXUIBackupContainer)
		if backupErr != nil {
			return fmt.Errorf("agent: inspect interrupted 3x-ui rollback during maintenance: %w", backupErr)
		}
		_, cleanupExists, cleanupErr := inspectThreeXUIContainer(ctx, docker, threeXUICleanupContainer)
		if cleanupErr != nil {
			return fmt.Errorf("agent: inspect committed 3x-ui cleanup during maintenance: %w", cleanupErr)
		}
		if candidateExists || backupExists || cleanupExists {
			return recoverInterruptedThreeXUIDeploy(ctx, docker)
		}
		return nil
	}
	_, candidateExists, err := inspectThreeXUIContainer(ctx, docker, threeXUICandidateContainer)
	if err != nil {
		return fmt.Errorf("agent: inspect committed 3x-ui candidate during maintenance: %w", err)
	}
	_, backupExists, err := inspectThreeXUIContainer(ctx, docker, threeXUIBackupContainer)
	if err != nil {
		return fmt.Errorf("agent: inspect committed 3x-ui rollback during maintenance: %w", err)
	}
	_, cleanupExists, err := inspectThreeXUIContainer(ctx, docker, threeXUICleanupContainer)
	if err != nil {
		return fmt.Errorf("agent: inspect committed 3x-ui cleanup during maintenance: %w", err)
	}
	if candidateExists || backupExists || cleanupExists {
		return recoverInterruptedThreeXUIDeploy(ctx, docker)
	}
	// A lone stopped canonical container may be an intentional keep-data
	// uninstall. Only an interruption marker authorizes maintenance to start it.
	return nil
}

func restoreInterruptedThreeXUIDeploy(ctx context.Context, docker threeXUIContainerEngine, current client.ContainerInspectResult, currentExists bool, backup client.ContainerInspectResult) error {
	snapshot, err := loadDurableThreeXUIDatabaseSnapshot(ctx, docker, backup.Container.ID)
	if err != nil {
		return fmt.Errorf("agent: load interrupted 3x-ui database rollback: %w", err)
	}
	if currentExists {
		if err := stopThreeXUIContainerForRecovery(ctx, docker, current, "incomplete promoted 3x-ui container"); err != nil {
			return err
		}
	} else if candidate, exists, inspectErr := inspectThreeXUIContainer(ctx, docker, threeXUICandidateContainer); inspectErr != nil {
		return fmt.Errorf("agent: inspect interrupted 3x-ui candidate: %w", inspectErr)
	} else if exists {
		if err := stopThreeXUIContainerForRecovery(ctx, docker, candidate, "interrupted 3x-ui candidate"); err != nil {
			return err
		}
	}
	// Once the incomplete current container is stopped it is no longer a
	// recoverable deployment. Remove its ambiguous canonical name before
	// touching the rollback container, so a failure below always leaves exactly
	// one durable recovery marker for the next maintenance pass.
	if currentExists {
		if _, err := docker.ContainerRemove(ctx, current.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("agent: remove incomplete promoted 3x-ui container: %w", err)
		}
		currentExists = false
	}
	if backup.Container.State != nil && backup.Container.State.Running {
		if err := stopThreeXUIContainerForRecovery(ctx, docker, backup, "3x-ui rollback container before restoring its database"); err != nil {
			return err
		}
	}
	if err := restoreThreeXUIDatabase(ctx, docker, backup.Container.ID, snapshot); err != nil {
		return fmt.Errorf("agent: restore interrupted 3x-ui database: %w", err)
	}
	// Start while the durable rollback/cleanup name still exists. If Start fails,
	// maintenance can identify and retry this recovery; a lone stopped canonical
	// name would be indistinguishable from an intentional keep-data uninstall.
	if err := startThreeXUIContainerForRecovery(ctx, docker, backup); err != nil {
		return err
	}
	if _, err := docker.ContainerRename(ctx, backup.Container.ID, client.ContainerRenameOptions{NewName: threeXUIContainer}); err != nil {
		restored, exists, inspectErr := inspectThreeXUIContainer(ctx, docker, threeXUIContainer)
		if inspectErr != nil || !exists || restored.Container.ID != backup.Container.ID {
			return errors.Join(fmt.Errorf("agent: restore interrupted 3x-ui deployment: %w", err), inspectErr)
		}
	}
	if err := removeThreeXUIContainerIfExists(ctx, docker, threeXUICandidateContainer); err != nil {
		return fmt.Errorf("agent: remove interrupted 3x-ui candidate: %w", err)
	}
	return nil
}

func startThreeXUIContainerForRecovery(ctx context.Context, docker threeXUIContainerEngine, inspected client.ContainerInspectResult) error {
	observed, inspectErr := docker.ContainerInspect(ctx, inspected.Container.ID, client.ContainerInspectOptions{})
	if inspectErr != nil {
		return fmt.Errorf("agent: inspect restored 3x-ui container before restart: %w", inspectErr)
	}
	if observed.Container.State != nil && observed.Container.State.Running {
		return nil
	}
	if _, err := docker.ContainerStart(ctx, inspected.Container.ID, client.ContainerStartOptions{}); err != nil && !errdefs.IsNotModified(err) {
		observed, inspectErr = docker.ContainerInspect(ctx, inspected.Container.ID, client.ContainerInspectOptions{})
		if inspectErr == nil && observed.Container.State != nil && observed.Container.State.Running {
			return nil
		}
		return errors.Join(fmt.Errorf("agent: restart restored 3x-ui container: %w", err), inspectErr)
	}
	return nil
}

func stopThreeXUIContainerForRecovery(ctx context.Context, docker threeXUIContainerEngine, inspected client.ContainerInspectResult, description string) error {
	if inspected.Container.State == nil || !inspected.Container.State.Running {
		return nil
	}
	if _, err := docker.ContainerStop(ctx, inspected.Container.ID, client.ContainerStopOptions{}); err != nil && !errdefs.IsNotModified(err) && !errdefs.IsNotFound(err) {
		observed, inspectErr := docker.ContainerInspect(ctx, inspected.Container.ID, client.ContainerInspectOptions{})
		if inspectErr == nil && observed.Container.State != nil && !observed.Container.State.Running {
			return nil
		}
		return errors.Join(fmt.Errorf("agent: stop %s: %w", description, err), inspectErr)
	}
	return nil
}

func inspectThreeXUIContainer(ctx context.Context, docker threeXUIContainerEngine, name string) (client.ContainerInspectResult, bool, error) {
	inspected, err := docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return client.ContainerInspectResult{}, false, nil
	}
	return inspected, err == nil, err
}

func removeThreeXUIContainerIfExists(ctx context.Context, docker threeXUIContainerEngine, name string) error {
	_, err := docker.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true})
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}
