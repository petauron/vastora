package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/moby/moby/client"
	"github.com/petauron/catalog/catalog"
)

// Meridian supplies its complete signed Xray configuration through a separate
// product API. Package installation stages only that product's verified image;
// an upgrade of an active instance must actually replace and verify its runtime.
// Both paths use the same receipt and Docker lifecycle as ordinary packages.
type meridianPackageBackend struct {
	*DockerPackageBackend
	Executor ApplicationExecutor
	Client   *client.Client
	state    *meridianRuntimeState
	image    string
}

func (b *meridianPackageBackend) Prepare(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if task.Operation == "uninstall" {
		return nil
	}
	if task.AppKey != meridianKey || b.Executor.Store == nil || len(task.Manifest.Images) != 1 || task.Manifest.Images[0].Name != "xray-core" {
		return errors.New("agent: invalid Meridian product package")
	}
	b.image = task.Manifest.Images[0].Reference
	if !validXrayWorkerImageReference(b.image) {
		return errors.New("agent: invalid Meridian runtime image identity")
	}
	if err := requireNoInterruptedXrayWorkerDeploy(ctx, b.Client); err != nil {
		return err
	}
	state, err := b.Executor.Store.loadMeridianRuntimeState(ctx)
	if err == nil {
		if state.ApplicationID != task.ApplicationID || state.Pending != nil || state.HandoverPending || state.Applied == nil {
			return errors.New("agent: Meridian runtime requires explicit recovery before package changes")
		}
		if _, err := b.Executor.observeAppliedMeridianRuntimeWithDocker(ctx, b.Client, state); err != nil {
			return err
		}
		b.state = &state
	} else if !errors.Is(err, errApplicationNotInstalled) {
		return err
	}
	pull, err := b.Client.ImagePull(ctx, b.image, client.ImagePullOptions{})
	if err != nil {
		return errors.New("agent: Meridian image download failed")
	}
	if err := pull.Wait(ctx); err != nil {
		_ = pull.Close()
		return errors.New("agent: Meridian image verification failed")
	}
	if err := pull.Close(); err != nil {
		return err
	}
	if b.state == nil {
		if current, _, exists, err := inspectCurrentMeridianRuntime(ctx, b.Client); err != nil || exists {
			return errors.Join(errors.New("agent: existing proxy requires explicit historical adoption or migration"), err)
		} else {
			_ = current
		}
		return nil
	}
	active := filepath.Join(b.Executor.Store.dataDir, meridianRuntimeDirectory, "config.json")
	encoded, err := os.ReadFile(active)
	if err != nil || !artifactMatchesBytes(*state.Applied, encoded) {
		return errors.New("agent: Meridian installed artifact cannot be proved")
	}
	// Validate the old signed configuration against the candidate image before
	// stopping any writer; package updates cannot silently discard product state.
	if err := validateXrayWorkerConfig(ctx, b.Client, b.image, active); err != nil {
		return err
	}
	hy2, err := meridianArtifactHY2Enabled(*state.Applied)
	if err != nil {
		return err
	}
	options := xrayWorkerContainerOptions(task, b.image, active, hy2, false)
	options.Name = meridianXrayContainer
	meridianContainerLandingPolicy(&options, state.AppliedPeers)
	b.plans = []packageContainerPlan{{logical: "xray", options: options}}
	return nil
}

func (b *meridianPackageBackend) Backup(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if b.state != nil {
		b.state.HandoverPending = true
		if err := b.Executor.Store.saveMeridianRuntimeState(ctx, *b.state); err != nil {
			return err
		}
		if err := b.Executor.Store.stopLandingMonitor(ctx); err != nil {
			return err
		}
		if err := closeMeridianGates(ctx, b.state.knownLandingGates(), newMeridianTrafficGate); err != nil {
			return err
		}
	}
	return b.DockerPackageBackend.Backup(ctx, task, receipt)
}

func (b *meridianPackageBackend) Apply(ctx context.Context, task DeploymentTask, receipt *InstanceResources, persist func() error) error {
	if b.state != nil {
		if err := b.DockerPackageBackend.Apply(ctx, task, receipt, persist); err != nil {
			return err
		}
	}
	receipt.Resources = slices.DeleteFunc(receipt.Resources, func(r RuntimeResource) bool { return r.Kind == "image" })
	receipt.Resources = append(receipt.Resources, RuntimeResource{Kind: "image", LogicalName: "xray-core", Name: b.image, SHA256: strings.Split(b.image, "@sha256:")[1]})
	if b.state == nil {
		receipt.IntegrationState = "awaiting-configuration"
	} else {
		receipt.IntegrationState = "active"
	}
	return persist()
}

func (b *meridianPackageBackend) Healthy(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if b.state == nil {
		return nil
	}
	resource := resourceNamed(receipt, "container", "xray")
	if resource == nil {
		return errors.New("agent: Meridian runtime receipt missing")
	}
	if err := waitForXrayWorkerRuntime(ctx, b.Client, resource.ID); err != nil {
		return err
	}
	b.state.ImageReference = b.image
	if _, err := b.Executor.observeAppliedMeridianRuntimeWithDocker(ctx, b.Client, *b.state); err != nil {
		return err
	}
	if err := b.Executor.Store.saveMeridianRuntimeState(ctx, *b.state); err != nil {
		return err
	}
	return b.Executor.finishMeridianLandingHandover(ctx, b.state)
}

func (b *meridianPackageBackend) Remove(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	state, err := b.Executor.Store.loadMeridianRuntimeState(ctx)
	if err != nil && !errors.Is(err, errApplicationNotInstalled) {
		return err
	}
	if err := b.Executor.Store.stopLandingMonitor(ctx); err != nil {
		return err
	}
	if err == nil {
		if err := removeSupersededMeridianGates(ctx, state.knownLandingGates(), nil, newMeridianTrafficGate); err != nil {
			return err
		}
	}
	if err := b.DockerPackageBackend.Remove(ctx, task, receipt); err != nil {
		return err
	}
	if _, err := b.Executor.Store.db.ExecContext(ctx, `DELETE FROM meridian_runtime_state WHERE id=1`); err != nil {
		return err
	}
	// Product configuration remains encrypted/in private state as recovery data;
	// a normal uninstall never recursively deletes the historical data directory.
	if task.DeleteData {
		if err := os.RemoveAll(filepath.Join(b.Executor.Store.dataDir, meridianRuntimeDirectory)); err != nil {
			return err
		}
	}
	receipt.Resources = slices.DeleteFunc(receipt.Resources, func(r RuntimeResource) bool { return r.Kind == "image" })
	return nil
}

// applyMeridianContainer is the integration boundary: product code prepares
// validated configuration/network policy, then hands all concrete container
// mutation and durable ownership to the generic runtime. No automatic rollback
// starts a previous executable after a candidate touched data.
func (e ApplicationExecutor) applyMeridianContainer(ctx context.Context, docker *client.Client, options client.ContainerCreateOptions, beforeApply func() error, validate func(string) (string, error), verify func(string, string) error) (result string, resultErr error) {
	executor := PackageExecutor{StateDirectory: e.packageDirectory()}
	applicationID := options.Config.Labels[applicationInstallationLabel]
	receipt, err := executor.ReadResources(applicationID)
	if err != nil || receipt.AppKey != meridianKey || receipt.State != "ready" {
		return "", errors.New("agent: Meridian runtime requires an adopted or newly installed package receipt")
	}
	if !receiptAuthorizesImage(receipt, options.Config.Image) {
		return "", errors.New("agent: Meridian image is not the installed signed package")
	}
	if err := requireNoInterruptedXrayWorkerDeploy(ctx, docker); err != nil {
		return "", err
	}
	options.Name = meridianXrayContainer
	task := DeploymentTask{ID: options.Config.Labels[applicationDeploymentIDLabel], AppKey: meridianKey, ApplicationID: applicationID, Operation: "configure"}
	backend := &DockerPackageBackend{Docker: docker, StateDirectory: e.packageDirectory(), plans: []packageContainerPlan{{logical: "xray", options: options}}}
	if err := backend.Inspect(ctx, task, receipt); err != nil {
		return "", err
	}
	receipt.State, receipt.TaskID = "applying", task.ID
	if err := executor.save(receipt); err != nil {
		return "", err
	}
	defer func() {
		if resultErr != nil {
			receipt.State = "review-required"
			resultErr = uncertainTaskOutcome(errors.Join(resultErr, executor.save(receipt)))
		}
	}()
	if err := backend.Backup(ctx, task, receipt); err != nil {
		return "", err
	}
	if beforeApply != nil {
		if err := beforeApply(); err != nil {
			return "", err
		}
	}
	// The complete product policy is supplied above; generic Apply only reads
	// Storage, which Meridian intentionally has no catalog-controlled volumes.
	task.Manifest.Runtime = &catalog.RuntimeSpec{Kind: "docker", Version: 1}
	if err := backend.Apply(ctx, task, receipt, func() error { return executor.save(receipt) }); err != nil {
		return "", err
	}
	resource := resourceNamed(receipt, "container", "xray")
	result, err = validate(resource.ID)
	if err != nil {
		return result, err
	}
	if err := verify(resource.ID, result); err != nil {
		return result, err
	}
	receipt.State, receipt.IntegrationState = "ready", "active"
	return result, executor.save(receipt)
}

func receiptAuthorizesImage(receipt *InstanceResources, image string) bool {
	for _, r := range receipt.Resources {
		if r.Kind == "image" && r.Name == image {
			return true
		}
	}
	// A historical container itself binds the original signed image digest.
	for _, r := range receipt.Resources {
		if r.Kind == "container" && r.SHA256 != "" && strings.HasSuffix(image, "@sha256:"+r.SHA256) {
			return true
		}
	}
	return false
}
