package agent

import (
	"context"
	"errors"
	"os"

	"github.com/moby/moby/client"
)

// A retained, deliberately stopped Docker source is not the live runtime of an
// installed native entry. Keep its records/counters, but never report them as a
// new health/usage sample. Native health and accounting arrive via Glivela.
func (e ApplicationExecutor) LegacyMeridianObservationRetired(ctx context.Context) (bool, error) {
	if e.Store == nil {
		return false, nil
	}
	old, err := e.Store.AppliedInstallation(ctx, meridianKey)
	if errors.Is(err, errApplicationNotInstalled) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return e.Store.executionRetired(old.ApplicationID)
}

func (s *Store) executionRetired(id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	receipt, err := (PackageExecutor{StateDirectory: s.dataDir}).ReadResources(id)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return receipt.IntegrationState == "execution-retired", nil
}

func (e ApplicationExecutor) verifyStoppedLegacyMeridian(ctx context.Context) (bool, error) {
	if e.Store == nil {
		return false, nil
	}
	native, err := e.Store.AppliedInstallation(ctx, "vastora-official/glivela-entry")
	if errors.Is(err, errApplicationNotInstalled) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if native.Manifest.Runtime == nil || native.Manifest.Runtime.Kind != "systemd" {
		return false, nil
	}
	receipt, err := (PackageExecutor{StateDirectory: e.packageDirectory()}).ReadResources(native.ApplicationID)
	if err != nil {
		return false, err
	}
	if receipt.AppKey != native.AppKey || receipt.Runtime != "systemd" || receipt.State != "ready" || receipt.PackageVersion != native.Version {
		return false, nil
	}
	state, err := e.Store.loadMeridianRuntimeState(ctx)
	if errors.Is(err, errApplicationNotInstalled) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return false, err
	}
	defer docker.Close()
	inspected, name, exists, err := inspectCurrentMeridianRuntime(ctx, docker)
	if err != nil {
		return false, err
	}
	c := inspected.Container
	if !exists || name != meridianXrayContainer || c.Config == nil || c.State == nil || c.HostConfig == nil {
		return false, nil
	}
	return c.Config.Labels[applicationInstallationLabel] == state.ApplicationID &&
		c.Config.Labels[applicationIdentityLabel] == meridianKey &&
		c.Config.Labels[xrayWorkerRuntimeLabel] == "xray" && c.Config.Image == state.ImageReference &&
		!c.State.Running && !c.State.Restarting && !c.State.Paused && !c.State.Dead && !c.State.OOMKilled && c.State.Pid == 0 &&
		!c.HostConfig.AutoRemove && c.HostConfig.RestartPolicy.Name == "no", nil
}
