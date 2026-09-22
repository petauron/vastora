package agent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/secret"
)

const (
	meridianRuntimeDirectory = "meridian"
	meridianRuntimeStateAAD  = "agent-meridian-runtime"
)

var errMeridianExplicitRecoveryRequired = errors.New("agent: Meridian runtime requires explicit recovery")

// meridianRuntimeState is the only durable Agent-side authority after
// cutover. Pending is journaled before Docker mutation; Applied is advanced
// only after the promoted container and its exact configuration are healthy.
type meridianRuntimeState struct {
	ApplicationID       string                    `json:"applicationId"`
	ImageReference      string                    `json:"imageReference"`
	Applied             *meridian.DesiredArtifact `json:"applied,omitempty"`
	Pending             *meridian.DesiredArtifact `json:"pending,omitempty"`
	AppliedPeers        []meridianruntime.Peer    `json:"appliedPeers,omitempty"`
	PendingPeers        []meridianruntime.Peer    `json:"pendingPeers,omitempty"`
	AppliedSource       *landing.PeerIdentity     `json:"appliedSource,omitempty"`
	PendingSource       *landing.PeerIdentity     `json:"pendingSource,omitempty"`
	Bridge              string                    `json:"bridge,omitempty"`
	RetiringGates       []meridianGateIdentity    `json:"retiringGates,omitempty"`
	HandoverPending     bool                      `json:"handoverPending,omitempty"`
	LegacyLandingSealed []byte                    `json:"legacyLandingSealed,omitempty"`
	LegacyRuntimeSHA256 string                    `json:"legacyRuntimeSha256,omitempty"`
}

func (state meridianRuntimeState) validate() error {
	if strings.TrimSpace(state.ApplicationID) == "" || !validXrayWorkerImageReference(state.ImageReference) || state.Applied == nil && state.Pending == nil {
		return errors.New("agent: invalid Meridian runtime state")
	}
	if state.Applied != nil && state.Applied.Validate() != nil || state.Pending != nil && state.Pending.Validate() != nil {
		return errors.New("agent: invalid Meridian runtime artifact")
	}
	if state.Applied != nil && state.Pending != nil && state.Pending.Revision <= state.Applied.Revision {
		return errors.New("agent: stale Meridian pending revision")
	}
	if err := state.validateLanding(); err != nil {
		return err
	}
	return nil
}

func (s *Store) saveMeridianRuntimeState(ctx context.Context, state meridianRuntimeState) error {
	if err := state.validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	sealed, err := secret.Seal(s.key, encoded, []byte(meridianRuntimeStateAAD))
	if err != nil {
		return fmt.Errorf("agent: encrypt Meridian runtime state: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO meridian_runtime_state(id,sealed_state) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET sealed_state=excluded.sealed_state`, sealed); err != nil {
		return fmt.Errorf("agent: save Meridian runtime state: %w", err)
	}
	return nil
}

func (s *Store) loadMeridianRuntimeState(ctx context.Context) (meridianRuntimeState, error) {
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT sealed_state FROM meridian_runtime_state WHERE id=1`).Scan(&sealed); errors.Is(err, sql.ErrNoRows) {
		return meridianRuntimeState{}, errApplicationNotInstalled
	} else if err != nil {
		return meridianRuntimeState{}, err
	}
	encoded, err := secret.Open(s.key, sealed, []byte(meridianRuntimeStateAAD))
	if err != nil {
		return meridianRuntimeState{}, errors.New("agent: Meridian runtime state does not match the local key")
	}
	var state meridianRuntimeState
	if json.Unmarshal(encoded, &state) != nil || state.validate() != nil {
		return meridianRuntimeState{}, errors.New("agent: persisted Meridian runtime state is invalid")
	}
	return state, nil
}

func (e ApplicationExecutor) recoverMeridianPendingState(ctx context.Context, state meridianRuntimeState, pendingImageReference string) (meridianRuntimeState, error) {
	if state.Pending == nil {
		return state, nil
	}
	if pendingImageReference != xrayWorkerImageReference {
		return state, errors.New("agent: Meridian pending image is not the audited Agent runtime")
	}
	active, err := os.ReadFile(filepath.Join(e.Store.dataDir, meridianRuntimeDirectory, "config.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && state.Applied == nil {
			// The task failed before it could stage its first configuration. Keep
			// the identical pending revision retryable, but do not advance it.
			return state, nil
		}
		return state, fmt.Errorf("%w: pending revision has no verifiable active configuration", errMeridianExplicitRecoveryRequired)
	}
	if state.Applied != nil && artifactMatchesBytes(*state.Applied, active) {
		state.RetiringGates = appendMeridianGates(state.RetiringGates, meridianPlanGates(state.PendingPeers, state.Bridge, state.Pending.Revision)...)
		state.Pending = nil
		state.PendingPeers = nil
		state.PendingSource = nil
		if err := e.Store.saveMeridianRuntimeState(ctx, state); err != nil {
			return state, err
		}
		return state, nil
	}
	if !artifactMatchesBytes(*state.Pending, active) {
		return state, fmt.Errorf("%w: pending revision does not match active or applied configuration", errMeridianExplicitRecoveryRequired)
	}
	candidate := state
	candidate.Applied, candidate.Pending = state.Pending, nil
	candidate.AppliedPeers, candidate.PendingPeers = slices.Clone(state.PendingPeers), nil
	candidate.AppliedSource, candidate.PendingSource = cloneMeridianSource(state.PendingSource), nil
	candidate.ImageReference = pendingImageReference
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return state, fmt.Errorf("agent: connect Docker for Meridian recovery: %w", err)
	}
	defer docker.Close()
	bootCandidate := state
	bootCandidate.ImageReference = pendingImageReference
	if err := e.prepareMeridianRuntimeStart(ctx, docker, bootCandidate, *state.Pending, state.PendingPeers); err != nil {
		return state, errors.Join(errMeridianExplicitRecoveryRequired, err)
	}
	if _, err := e.observeAppliedMeridianRuntimeWithDocker(ctx, docker, candidate); err != nil {
		return state, errors.Join(errMeridianExplicitRecoveryRequired, errors.New("agent: Meridian pending revision is not a healthy promoted runtime"), err)
	}
	if err := e.cleanupCompletedMeridianReplacement(ctx, docker, candidate); err != nil {
		return state, errors.Join(errMeridianExplicitRecoveryRequired, err)
	}
	if err := e.Store.saveMeridianRuntimeState(ctx, candidate); err != nil {
		return state, err
	}
	return candidate, nil
}

// replaceMeridianPendingState is deliberately reachable only through an
// operator-authorized Center task. It replaces an uncertain journal entry
// with the complete current Center projection; the active file is still kept
// as the transactional rollback input until the new container is healthy.
func (e ApplicationExecutor) replaceMeridianPendingState(ctx context.Context, state meridianRuntimeState, task meridianruntime.Task) (meridianRuntimeState, error) {
	if !task.ReplacePendingState || state.Pending == nil {
		return state, errors.New("agent: Meridian pending-state replacement was not authorized")
	}
	if state.Applied != nil && task.Desired.Revision <= state.Applied.Revision {
		return state, errors.New("agent: Meridian recovery revision is not newer than the applied receipt")
	}
	if task.Desired.Revision < state.Pending.Revision {
		return state, errors.New("agent: Meridian recovery would replace a newer pending revision")
	}
	candidate := state
	desired := task.Desired
	candidate.RetiringGates = candidate.knownLandingGates()
	candidate.Pending = &desired
	candidate.PendingPeers = slices.Clone(task.Peers)
	candidate.PendingSource = cloneMeridianSource(task.Source)
	candidate.ImageReference = task.ImageReference
	if err := e.Store.saveMeridianRuntimeState(ctx, candidate); err != nil {
		return state, err
	}
	return candidate, nil
}

func (e ApplicationExecutor) ApplyMeridianRuntime(ctx context.Context, task meridianruntime.Task) (result meridianruntime.Result, resultErr error) {
	if e.Store == nil || task.Validate() != nil || task.ImageReference != xrayWorkerImageReference {
		return result, errors.New("agent: invalid Meridian runtime task")
	}
	state, err := e.Store.loadMeridianRuntimeState(ctx)
	if errors.Is(err, errApplicationNotInstalled) {
		state = meridianRuntimeState{ApplicationID: task.ApplicationID, ImageReference: task.ImageReference}
	} else if err != nil {
		return result, err
	}
	if state.ApplicationID != task.ApplicationID {
		return result, errors.New("agent: Meridian runtime belongs to another application")
	}
	if state.Pending != nil || state.HandoverPending {
		if err := e.Store.stopLandingMonitor(ctx); err != nil {
			return result, err
		}
		if err := closeMeridianGates(ctx, state.knownLandingGates(), newMeridianTrafficGate); err != nil {
			return result, err
		}
		if err := e.Store.stopXrayWorkerAPI(ctx, false); err != nil {
			return result, err
		}
	}
	if state.Pending != nil {
		state, err = e.recoverMeridianPendingState(ctx, state, task.ImageReference)
		if err != nil {
			if !task.ReplacePendingState || !errors.Is(err, errMeridianExplicitRecoveryRequired) {
				return result, err
			}
			state, err = e.replaceMeridianPendingState(ctx, state, task)
			if err != nil {
				return result, err
			}
		}
	}
	if state.Applied != nil && sameMeridianArtifact(*state.Applied, task.Desired) && state.ImageReference == task.ImageReference {
		if !sameMeridianPeers(state.AppliedPeers, task.Peers) || !sameMeridianSource(state.AppliedSource, task.Source) {
			return result, errors.New("agent: Meridian applied peer identity changed without a revision")
		}
		if state.HandoverPending {
			if err := e.finishMeridianLandingHandover(ctx, &state); err != nil {
				return result, uncertainTaskOutcome(err)
			}
		}
		if task.RetireLegacy {
			if err := e.Store.verifyMeridianLegacyRetirement(ctx, task.ApplicationID); err != nil {
				return result, err
			}
			if err := e.ensureCleanMeridianContainerIdentity(ctx, task, state); err != nil {
				return result, err
			}
		}
		result, err := e.observeAppliedMeridianRuntime(ctx, state)
		if err != nil {
			return result, err
		}
		if !e.Store.meridianLandingMonitorRunning(state) {
			if err := e.startMeridianLandingMonitor(ctx, state); err != nil {
				return result, err
			}
			result.Peers = e.Store.meridianPeerObservations(state)
		}
		if task.RetireLegacy {
			if err := waitForMeridianRetirementPeers(ctx, task, func(observeContext context.Context) (meridianruntime.Result, error) {
				return e.observeAppliedMeridianRuntime(observeContext, state)
			}); err != nil {
				return result, err
			}
			result, err = e.observeAppliedMeridianRuntime(ctx, state)
			if err != nil {
				return result, err
			}
			health, err := result.PeerHealth(task, time.Now().UTC())
			if err != nil {
				return result, err
			}
			for _, ready := range health {
				if !ready {
					return result, errors.New("agent: Meridian peer became unavailable before legacy retirement")
				}
			}
		}
		if err := e.retireLegacyMeridianInstallation(ctx, task.ApplicationID, task.RetireLegacy); err != nil {
			return result, err
		}
		result.LegacyRetired, err = e.Store.legacyMeridianRetired(ctx, task.ApplicationID)
		return result, err
	}
	if state.Pending != nil && (!sameMeridianArtifact(*state.Pending, task.Desired) || !sameMeridianPeers(state.PendingPeers, task.Peers) || !sameMeridianSource(state.PendingSource, task.Source)) {
		if !task.ReplacePendingState {
			return result, errors.New("agent: another Meridian revision requires explicit recovery")
		}
		state, err = e.replaceMeridianPendingState(ctx, state, task)
		if err != nil {
			return result, err
		}
	}
	if task.RetireLegacy {
		return result, errors.New("agent: legacy retirement requires the already verified Meridian revision")
	}
	if state.Applied != nil && task.Desired.Revision <= state.Applied.Revision {
		return result, errors.New("agent: Meridian runtime revision is stale")
	}
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return result, fmt.Errorf("agent: connect Docker for Meridian: %w", err)
	}
	defer docker.Close()
	pull, err := docker.ImagePull(ctx, task.ImageReference, client.ImagePullOptions{})
	if err != nil {
		return result, errors.New("agent: pull audited Meridian Xray image")
	}
	if err := pull.Wait(ctx); err != nil {
		_ = pull.Close()
		return result, errors.New("agent: pull audited Meridian Xray image")
	}
	if err := pull.Close(); err != nil {
		return result, errors.New("agent: close Meridian image pull")
	}
	hy2Enabled, err := meridianArtifactHY2Enabled(task.Desired)
	if err != nil {
		return result, err
	}

	staged, active, err := e.Store.stageMeridianConfig(task.Desired.Config)
	if err != nil {
		return result, err
	}
	defer os.Remove(staged)
	if err := validateXrayWorkerConfig(ctx, docker, task.ImageReference, staged); err != nil {
		return result, err
	}
	if len(task.Peers) != 0 {
		if err := ensureLandingPackage(ctx); err != nil {
			return result, err
		}
	}
	previousActive, _ := os.ReadFile(active)
	if state.Applied != nil && !artifactMatchesBytes(*state.Applied, previousActive) && !task.ReplacePendingState {
		return result, errors.New("agent: active Meridian configuration does not match its applied receipt")
	}
	deployment := DeploymentTask{ID: "meridian-runtime-r" + fmt.Sprint(task.Desired.Revision), AppKey: meridianKey, ApplicationID: task.ApplicationID}
	options := xrayWorkerContainerOptions(deployment, task.ImageReference, active, hy2Enabled, task.PreserveLegacyAliases)
	meridianContainerLandingPolicy(&options, task.Peers)
	restore := func(recoveryContext context.Context) error {
		if len(previousActive) == 0 {
			return nil
		}
		return e.Store.writeExactMeridianConfig(previousActive)
	}
	sha, err := replaceXrayWorkerContainer(ctx, docker, options, func() error {
		if err := e.beginMeridianLandingHandover(ctx, docker, &state, task); err != nil {
			return err
		}
		if err := commitXrayWorkerConfig(staged, active); err != nil {
			return fmt.Errorf("agent: commit Meridian configuration: %w", err)
		}
		return nil
	}, func(containerID string) (string, error) {
		if err := waitForXrayWorkerRuntime(ctx, docker, containerID); err != nil {
			return "", err
		}
		return task.Desired.ConfigSHA256, nil
	}, func(_ string, observed string) error {
		if subtle.ConstantTimeCompare([]byte(observed), []byte(task.Desired.ConfigSHA256)) != 1 {
			return errors.New("agent: promoted Meridian configuration digest changed")
		}
		return nil
	}, restore)
	if err != nil {
		return result, err
	}
	if subtle.ConstantTimeCompare([]byte(sha), []byte(task.Desired.ConfigSHA256)) != 1 {
		return result, uncertainTaskOutcome(errors.New("agent: Meridian runtime promotion returned the wrong digest"))
	}
	applied := task.Desired
	state.Applied, state.Pending = &applied, nil
	state.AppliedPeers, state.PendingPeers = slices.Clone(task.Peers), nil
	state.AppliedSource, state.PendingSource = cloneMeridianSource(task.Source), nil
	state.ImageReference = task.ImageReference
	if err := e.Store.saveMeridianRuntimeState(ctx, state); err != nil {
		return result, uncertainTaskOutcome(err)
	}
	if err := e.finishMeridianLandingHandover(ctx, &state); err != nil {
		return result, uncertainTaskOutcome(err)
	}
	if err := e.retireLegacyMeridianInstallation(ctx, task.ApplicationID, task.RetireLegacy); err != nil {
		return result, uncertainTaskOutcome(err)
	}
	result, err = e.observeAppliedMeridianRuntimeWithDocker(ctx, docker, state)
	if err == nil {
		result.LegacyRetired, err = e.Store.legacyMeridianRetired(ctx, task.ApplicationID)
	}
	return result, err
}

// ensureCleanMeridianContainerIdentity removes migration-only Docker aliases
// only after Center has confirmed that public entries target its native
// subscription service and the Meridian runtime. The exact applied artifact is
// promoted again, so this operation cannot introduce a different Xray config.
func (e ApplicationExecutor) ensureCleanMeridianContainerIdentity(ctx context.Context, task meridianruntime.Task, state meridianRuntimeState) error {
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return fmt.Errorf("agent: connect Docker for Meridian identity cleanup: %w", err)
	}
	defer docker.Close()
	if err := e.cleanupCompletedMeridianReplacement(ctx, docker, state); err != nil {
		return err
	}
	current, exists, err := inspectXrayWorkerContainer(ctx, docker, meridianXrayContainer)
	if err != nil || !exists || current.Container.Config == nil || current.Container.Config.Labels[applicationIdentityLabel] != meridianKey || current.Container.NetworkSettings == nil {
		return errors.Join(errors.New("agent: Meridian runtime identity is unavailable"), err)
	}
	endpoint := current.Container.NetworkSettings.Networks[dockerruntime.NetworkName]
	if endpoint == nil {
		return errors.New("agent: Meridian runtime bridge attachment is unavailable")
	}
	legacyAlias := slices.Contains(endpoint.Aliases, dockerruntime.LegacyXrayAlias) || slices.Contains(endpoint.Aliases, dockerruntime.ThreeXUIAlias)
	if !legacyAlias {
		return nil
	}
	active := filepath.Join(e.Store.dataDir, meridianRuntimeDirectory, "config.json")
	encoded, err := os.ReadFile(active)
	if err != nil || state.Applied == nil || !artifactMatchesBytes(*state.Applied, encoded) || !sameMeridianArtifact(*state.Applied, task.Desired) {
		return errors.New("agent: Meridian runtime cannot prove its applied artifact before identity cleanup")
	}
	deployment := DeploymentTask{ID: "meridian-retire-r" + fmt.Sprint(task.Desired.Revision), AppKey: meridianKey, ApplicationID: task.ApplicationID}
	hy2Enabled, err := meridianArtifactHY2Enabled(task.Desired)
	if err != nil {
		return err
	}
	options := xrayWorkerContainerOptions(deployment, task.ImageReference, active, hy2Enabled, false)
	meridianContainerLandingPolicy(&options, state.AppliedPeers)
	sha, err := replaceXrayWorkerContainer(ctx, docker, options, func() error {
		state.HandoverPending = true
		if err := e.Store.saveMeridianRuntimeState(ctx, state); err != nil {
			return err
		}
		if err := e.Store.stopLandingMonitor(ctx); err != nil {
			return err
		}
		return closeMeridianGates(ctx, state.knownLandingGates(), newMeridianTrafficGate)
	}, func(containerID string) (string, error) {
		if err := waitForXrayWorkerRuntime(ctx, docker, containerID); err != nil {
			return "", err
		}
		return task.Desired.ConfigSHA256, nil
	}, func(_ string, observed string) error {
		if subtle.ConstantTimeCompare([]byte(observed), []byte(task.Desired.ConfigSHA256)) != 1 {
			return errors.New("agent: cleaned Meridian runtime digest changed")
		}
		return nil
	}, nil)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(sha), []byte(task.Desired.ConfigSHA256)) != 1 {
		return uncertainTaskOutcome(errors.New("agent: Meridian identity cleanup returned the wrong digest"))
	}
	if err := e.finishMeridianLandingHandover(ctx, &state); err != nil {
		return uncertainTaskOutcome(err)
	}
	return nil
}

// cleanupCompletedMeridianReplacement removes only stopped predecessors left
// after the new Meridian container was already promoted. The current runtime,
// application ownership, image, and exact applied artifact are proved first;
// candidates are never removed by this path because they represent an
// unfinished promotion rather than completed cleanup.
func (e ApplicationExecutor) cleanupCompletedMeridianReplacement(ctx context.Context, docker threeXUIContainerEngine, state meridianRuntimeState) error {
	if state.Applied == nil {
		return errors.New("agent: Meridian cleanup has no applied revision")
	}
	current, exists, err := inspectXrayWorkerContainer(ctx, docker, meridianXrayContainer)
	if err != nil || !exists || current.Container.Config == nil || current.Container.State == nil || !current.Container.State.Running || current.Container.Config.Labels[applicationIdentityLabel] != meridianKey || current.Container.Config.Labels[applicationInstallationLabel] != state.ApplicationID || current.Container.Config.Image != state.ImageReference {
		return errors.Join(errors.New("agent: Meridian cleanup cannot prove the current runtime"), err)
	}
	active, err := os.ReadFile(filepath.Join(e.Store.dataDir, meridianRuntimeDirectory, "config.json"))
	if err != nil || !artifactMatchesBytes(*state.Applied, active) {
		return errors.Join(errors.New("agent: Meridian cleanup cannot prove the applied configuration"), err)
	}
	if _, candidateExists, err := inspectOwnedProxyRuntimeContainer(ctx, docker, meridianXrayCandidateContainer); err != nil {
		return err
	} else if candidateExists {
		return errors.New("agent: Meridian candidate still requires explicit recovery")
	}
	for _, name := range []string{meridianXrayBackupContainer, meridianXrayCleanupContainer} {
		residue, residueExists, err := inspectOwnedProxyRuntimeContainer(ctx, docker, name)
		if err != nil {
			return err
		}
		if !residueExists {
			continue
		}
		if residue.Container.Config == nil || residue.Container.Config.Labels[applicationInstallationLabel] != state.ApplicationID || residue.Container.State == nil || residue.Container.State.Running {
			return errors.New("agent: Meridian replacement residue requires explicit review")
		}
		if _, err := docker.ContainerRemove(ctx, residue.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("agent: remove completed Meridian replacement residue: %w", err)
		}
	}
	return nil
}

func (e ApplicationExecutor) retireLegacyMeridianInstallation(ctx context.Context, applicationID string, authorized bool) error {
	if e.Store == nil {
		return errors.New("agent: Meridian runtime store is unavailable")
	}
	legacy, err := e.Store.AppliedInstallation(ctx, threeXUIKey)
	if errors.Is(err, errApplicationNotInstalled) {
		if authorized {
			return e.Store.retireMeridianLegacyLanding(ctx, applicationID)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if legacy.ApplicationID != applicationID {
		return errors.New("agent: legacy proxy installation belongs to another application")
	}
	if !authorized {
		// The first Meridian revision is deliberately verified while the legacy
		// installation receipt is still retained as retirement evidence.
		// Retirement is a separate Center-authorized receipt operation after
		// subscription authority has switched to Meridian.
		return nil
	}
	if err := e.Store.verifyMeridianLegacyRetirement(ctx, applicationID); err != nil {
		return err
	}
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return fmt.Errorf("agent: connect Docker for legacy Meridian retirement: %w", err)
	}
	defer docker.Close()
	if err := validateThreeXUIOwnership(ctx, docker, applicationID); err != nil {
		return err
	}
	for _, name := range []string{threeXUICandidateContainer, threeXUIBackupContainer, threeXUICleanupContainer, threeXUIContainer} {
		if err := removeOwnedApplicationContainer(ctx, docker, name, threeXUIKey, "3x-ui", applicationID, anyApplicationDeployment); err != nil {
			return fmt.Errorf("agent: retire legacy 3x-ui container: %w", err)
		}
	}
	for _, name := range applicationVolumes[threeXUIKey] {
		if err := removeOwnedApplicationVolume(ctx, docker, name, threeXUIKey, applicationVolumeComponent(name), applicationID); err != nil {
			return fmt.Errorf("agent: retire legacy 3x-ui volume: %w", err)
		}
	}
	if err := e.Store.stopXrayWorkerAPI(ctx, false); err != nil {
		return err
	}
	if err := e.Store.retireXrayWorkerState(ctx); err != nil {
		return err
	}
	if err := e.Store.retireMeridianLegacyLanding(ctx, applicationID); err != nil {
		return err
	}
	return e.Store.RemoveApplied(ctx, threeXUIKey)
}

func (e ApplicationExecutor) RetireLegacyMeridianInstallation(ctx context.Context, task meridianruntime.LegacyRetireTask) (meridianruntime.LegacyRetireResult, error) {
	if task.Validate() != nil {
		return meridianruntime.LegacyRetireResult{}, errors.New("agent: invalid Meridian legacy retirement task")
	}
	if err := e.retireLegacyMeridianInstallation(ctx, task.ApplicationID, true); err != nil {
		return meridianruntime.LegacyRetireResult{}, err
	}
	retired, err := e.Store.legacyMeridianRetired(ctx, task.ApplicationID)
	if err != nil {
		return meridianruntime.LegacyRetireResult{}, err
	}
	result := meridianruntime.LegacyRetireResult{LegacyRetired: retired}
	if result.Validate() != nil {
		return meridianruntime.LegacyRetireResult{}, errors.New("agent: legacy controller installation is still present")
	}
	return result, nil
}

func (s *Store) legacyMeridianRetired(ctx context.Context, applicationID string) (bool, error) {
	legacy, err := s.AppliedInstallation(ctx, threeXUIKey)
	if errors.Is(err, errApplicationNotInstalled) {
		var retained int
		if err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM xray_worker_state)+(SELECT COUNT(*) FROM landing_runtime_state)`).Scan(&retained); err != nil {
			return false, err
		}
		return retained == 0, nil
	}
	if err != nil {
		return false, err
	}
	if legacy.ApplicationID != applicationID {
		return false, errors.New("agent: legacy proxy installation belongs to another application")
	}
	return false, nil
}

func (s *Store) removeMeridianRuntimeState(ctx context.Context) error {
	state, err := s.loadMeridianRuntimeState(ctx)
	if err != nil && !errors.Is(err, errApplicationNotInstalled) {
		return err
	}
	if err := s.stopLandingMonitor(ctx); err != nil {
		return err
	}
	if err := removeSupersededMeridianGates(ctx, state.knownLandingGates(), nil, newMeridianTrafficGate); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM meridian_runtime_state WHERE id=1`); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.dataDir, meridianRuntimeDirectory))
}

func (e ApplicationExecutor) observeAppliedMeridianRuntime(ctx context.Context, state meridianRuntimeState) (meridianruntime.Result, error) {
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return meridianruntime.Result{}, err
	}
	defer docker.Close()
	return e.observeAppliedMeridianRuntimeWithDocker(ctx, docker, state)
}

func (e ApplicationExecutor) ObserveMeridianRuntime(ctx context.Context) (meridianruntime.Result, error) {
	if e.Store == nil {
		return meridianruntime.Result{}, errors.New("agent: Meridian runtime store is unavailable")
	}
	state, err := e.Store.loadMeridianRuntimeState(ctx)
	if err != nil {
		return meridianruntime.Result{}, err
	}
	result, err := e.observeAppliedMeridianRuntime(ctx, state)
	if err == nil {
		result.LegacyRetired, err = e.Store.legacyMeridianRetired(ctx, state.ApplicationID)
	}
	return result, err
}

func (e ApplicationExecutor) observeAppliedMeridianRuntimeWithDocker(ctx context.Context, docker *client.Client, state meridianRuntimeState) (meridianruntime.Result, error) {
	if state.Applied == nil {
		return meridianruntime.Result{}, errors.New("agent: Meridian runtime has no applied revision")
	}
	inspected, runtimeName, exists, err := inspectCurrentMeridianRuntime(ctx, docker)
	if err != nil || !exists || runtimeName != meridianXrayContainer || inspected.Container.Config == nil || inspected.Container.State == nil || !inspected.Container.State.Running || inspected.Container.Config.Labels[xrayWorkerRuntimeLabel] != "xray" || inspected.Container.Config.Labels[applicationIdentityLabel] != meridianKey || inspected.Container.Config.Labels[applicationInstallationLabel] != state.ApplicationID || inspected.Container.Config.Image != state.ImageReference {
		return meridianruntime.Result{}, errors.Join(errors.New("agent: Meridian Xray runtime is unavailable"), err)
	}
	if err := e.verifyMeridianRuntimeAttachment(ctx, docker, inspected, state, state.AppliedPeers); err != nil {
		return meridianruntime.Result{}, err
	}
	active, err := os.ReadFile(filepath.Join(e.Store.dataDir, meridianRuntimeDirectory, "config.json"))
	if err != nil || !artifactMatchesBytes(*state.Applied, active) {
		return meridianruntime.Result{}, errors.New("agent: active Meridian configuration does not match its applied revision")
	}
	stats, err := runXrayWorkerCommand(ctx, docker, inspected.Container.ID, []string{"xray", "api", "statsquery", "--server=127.0.0.1:10085", "-pattern", "user>>>", "-reset=false"})
	if err != nil || !json.Valid(stats) {
		return meridianruntime.Result{}, errors.Join(errors.New("agent: read Meridian Xray statistics"), err)
	}
	result := meridianruntime.Result{Receipt: meridian.AppliedReceipt{Revision: state.Applied.Revision, ConfigSHA256: state.Applied.ConfigSHA256, RuntimeReady: true}, Stats: json.RawMessage(stats), Peers: e.Store.meridianPeerObservations(state), Source: cloneMeridianSource(state.AppliedSource)}
	if err := result.Validate(*state.Applied); err != nil {
		return meridianruntime.Result{}, err
	}
	return result, nil
}

func (s *Store) stageMeridianConfig(encoded []byte) (string, string, error) {
	if len(encoded) == 0 || len(encoded) > xrayWorkerMaxBody || !json.Valid(encoded) {
		return "", "", errors.New("agent: invalid Meridian Xray configuration")
	}
	directory := filepath.Join(s.dataDir, meridianRuntimeDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", "", err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", "", err
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(directory, xrayWorkerRuntimeUID(), -1); err != nil {
			return "", "", err
		}
	}
	temporary, err := os.CreateTemp(directory, "candidate-*.json")
	if err != nil {
		return "", "", err
	}
	name := temporary.Name()
	if os.Geteuid() == 0 {
		err = temporary.Chown(xrayWorkerRuntimeUID(), -1)
	}
	if err == nil {
		err = temporary.Chmod(0o600)
	}
	if err == nil {
		_, err = temporary.Write(encoded)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", "", err
	}
	return name, filepath.Join(directory, "config.json"), nil
}

func (s *Store) writeExactMeridianConfig(encoded []byte) error {
	staged, active, err := s.stageMeridianConfig(encoded)
	if err != nil {
		return err
	}
	defer os.Remove(staged)
	return commitXrayWorkerConfig(staged, active)
}

func sameMeridianArtifact(left, right meridian.DesiredArtifact) bool {
	return left.Revision == right.Revision && subtle.ConstantTimeCompare([]byte(left.ConfigSHA256), []byte(right.ConfigSHA256)) == 1
}

func artifactMatchesBytes(artifact meridian.DesiredArtifact, encoded []byte) bool {
	if artifact.Validate() != nil || len(encoded) == 0 || len(encoded) > xrayWorkerMaxBody {
		return false
	}
	digest := sha256.Sum256(encoded)
	return subtle.ConstantTimeCompare([]byte(artifact.ConfigSHA256), []byte(hex.EncodeToString(digest[:]))) == 1
}

func meridianArtifactHY2Enabled(artifact meridian.DesiredArtifact) (bool, error) {
	if artifact.Validate() != nil {
		return false, errors.New("agent: invalid Meridian runtime artifact")
	}
	var config struct {
		Inbounds []struct {
			Protocol string `json:"protocol"`
			Port     int    `json:"port"`
		} `json:"inbounds"`
	}
	if json.Unmarshal(artifact.Config, &config) != nil {
		return false, errors.New("agent: invalid Meridian Xray configuration")
	}
	for _, inbound := range config.Inbounds {
		if inbound.Protocol == "hysteria" {
			if inbound.Port != threeXUIRealityPort {
				return false, errors.New("agent: Meridian Hysteria runtime does not use UDP 443")
			}
			return true, nil
		}
	}
	return false, nil
}
