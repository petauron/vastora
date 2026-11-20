package agent

import (
	"context"
	"errors"
	"time"

	"github.com/petauron/vastora/internal/glivelaruntime"
)

// ResumeAppliedGlivelaMonitor only observes the retained running container
// and resumes its lease-bound probes. It never calls the runtime apply path,
// starts a container, writes configuration, or retires an installation.
func (e ApplicationExecutor) ResumeAppliedGlivelaMonitor(ctx context.Context, task glivelaruntime.Task) (glivelaruntime.Result, error) {
	if e.Store == nil || !task.ResumeApplied || task.Validate() != nil {
		return glivelaruntime.Result{}, errors.New("agent: invalid applied monitor recovery")
	}
	e.Store.landingMutationMu.Lock()
	defer e.Store.landingMutationMu.Unlock()
	if retired, err := e.Store.executionRetired(task.ApplicationID); err != nil || retired {
		return glivelaruntime.Result{}, errors.Join(errors.New("agent: retired execution cannot resume legacy monitoring"), err)
	}
	state, err := e.Store.loadGlivelaRuntimeState(ctx)
	if err != nil || state.Pending != nil || state.HandoverPending || len(state.RetiringGates) != 0 ||
		state.ApplicationID != task.ApplicationID || state.Applied == nil ||
		!sameGlivelaArtifact(*state.Applied, task.Desired) || state.ImageReference != task.ImageReference ||
		!sameGlivelaPeers(state.AppliedPeers, task.Peers) || !sameGlivelaSource(state.AppliedSource, task.Source) {
		return glivelaruntime.Result{}, errors.New("agent: monitor recovery requires the identical applied runtime")
	}
	installation, err := e.Store.AppliedInstallation(ctx, legacyProxyAppKey)
	if err != nil || installation.ApplicationID != task.ApplicationID {
		return glivelaruntime.Result{}, errors.New("agent: monitor recovery installation does not match")
	}
	receipt, err := e.ApplicationResources(task.ApplicationID)
	if err != nil || !receiptAuthorizesImage(receipt, task.ImageReference) {
		return glivelaruntime.Result{}, errors.New("agent: monitor recovery image is not authorized")
	}
	result, err := e.observeAppliedGlivelaRuntime(ctx, state)
	if err != nil {
		return result, err
	}
	if !e.Store.glivelaLandingMonitorRunning(state) {
		if err := e.startGlivelaLandingMonitor(ctx, state); err != nil {
			return result, err
		}
	}
	result.Peers = e.Store.glivelaPeerObservations(state)
	if len(task.EgressClients) != 0 {
		evidence := &glivelaruntime.EgressObservation{Policy: task.NativeEgress, ConfigSHA256: task.Desired.ConfigSHA256}
		for _, client := range task.EgressClients {
			exit, err := e.verifyGlivelaEgressClient(ctx, client, task.ImageReference, task.NativeEgress)
			if err != nil {
				return result, err
			}
			evidence.Exits = append(evidence.Exits, exit)
		}
		evidence.CheckedAt = time.Now().UTC()
		result.Egress = evidence
	}
	result.LegacyRetired, err = e.Store.legacyGlivelaRetired(ctx, task.ApplicationID)
	return result, err
}
