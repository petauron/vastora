package agent

import (
	"context"
	"errors"
	"time"

	"github.com/petauron/vastora/internal/meridianruntime"
)

// ResumeAppliedMeridianMonitor only observes the retained running container
// and resumes its lease-bound probes. It never calls the runtime apply path,
// starts a container, writes configuration, or retires an installation.
func (e ApplicationExecutor) ResumeAppliedMeridianMonitor(ctx context.Context, task meridianruntime.Task) (meridianruntime.Result, error) {
	if e.Store == nil || !task.ResumeApplied || task.Validate() != nil {
		return meridianruntime.Result{}, errors.New("agent: invalid applied monitor recovery")
	}
	e.Store.landingMutationMu.Lock()
	defer e.Store.landingMutationMu.Unlock()
	if retired, err := e.Store.executionRetired(task.ApplicationID); err != nil || retired {
		return meridianruntime.Result{}, errors.Join(errors.New("agent: retired execution cannot resume legacy monitoring"), err)
	}
	state, err := e.Store.loadMeridianRuntimeState(ctx)
	if err != nil || state.Pending != nil || state.HandoverPending || len(state.RetiringGates) != 0 ||
		state.ApplicationID != task.ApplicationID || state.Applied == nil ||
		!sameMeridianArtifact(*state.Applied, task.Desired) || state.ImageReference != task.ImageReference ||
		!sameMeridianPeers(state.AppliedPeers, task.Peers) || !sameMeridianSource(state.AppliedSource, task.Source) {
		return meridianruntime.Result{}, errors.New("agent: monitor recovery requires the identical applied runtime")
	}
	installation, err := e.Store.AppliedInstallation(ctx, meridianKey)
	if err != nil || installation.ApplicationID != task.ApplicationID {
		return meridianruntime.Result{}, errors.New("agent: monitor recovery installation does not match")
	}
	receipt, err := e.ApplicationResources(task.ApplicationID)
	if err != nil || !receiptAuthorizesImage(receipt, task.ImageReference) {
		return meridianruntime.Result{}, errors.New("agent: monitor recovery image is not authorized")
	}
	result, err := e.observeAppliedMeridianRuntime(ctx, state)
	if err != nil {
		return result, err
	}
	if !e.Store.meridianLandingMonitorRunning(state) {
		if err := e.startMeridianLandingMonitor(ctx, state); err != nil {
			return result, err
		}
	}
	result.Peers = e.Store.meridianPeerObservations(state)
	if len(task.EgressClients) != 0 {
		evidence := &meridianruntime.EgressObservation{Policy: task.NativeEgress, ConfigSHA256: task.Desired.ConfigSHA256}
		for _, client := range task.EgressClients {
			exit, err := e.verifyMeridianEgressClient(ctx, client, task.ImageReference, task.NativeEgress)
			if err != nil {
				return result, err
			}
			evidence.Exits = append(evidence.Exits, exit)
		}
		evidence.CheckedAt = time.Now().UTC()
		result.Egress = evidence
	}
	result.LegacyRetired, err = e.Store.legacyMeridianRetired(ctx, task.ApplicationID)
	return result, err
}
