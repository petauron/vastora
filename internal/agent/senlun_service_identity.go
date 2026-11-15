package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/senulun"
)

func (e ApplicationExecutor) senlunIdentityDocker() (packageDockerEngine, func(), error) {
	if e.PackageBackend != nil {
		backend, ok := e.PackageBackend.(*DockerPackageBackend)
		if !ok || backend.Docker == nil {
			return nil, nil, errors.New("agent: Senlun Docker ownership inspection is unavailable")
		}
		return backend.Docker, func() {}, nil
	}
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return nil, nil, errors.New("agent: Senlun Docker executor is unavailable")
	}
	return docker, func() { _ = docker.Close() }, nil
}

// renameSenlunServiceIdentity copies the live data directly into a new owned
// Docker volume. It neither creates a data backup nor initializes a new store.
// The stopped source remains untouched until Center confirms the new runtime.
func (e ApplicationExecutor) renameSenlunServiceIdentity(ctx context.Context, task DeploymentTask) (result ApplicationTaskResult, resultErr error) {
	if e.Store == nil || !senlunServiceIdentityTask(task) {
		return result, errors.New("agent: Senlun service identity migration is not authorized")
	}
	const oldKey = "vastora-official/pulse"
	const newKey = "vastora-official/senlun"
	previous, err := e.Store.AppliedInstallation(ctx, oldKey)
	if err != nil || previous.ApplicationID != task.ApplicationID {
		return result, errors.New("agent: original Senlun service identity is unavailable")
	}
	if err := senulun.ValidateIdentityRename(previous.Manifest, task.Manifest); err != nil {
		return result, err
	}
	if _, err := e.Store.AppliedInstallation(ctx, newKey); !errors.Is(err, errApplicationNotInstalled) {
		return result, errors.New("agent: target Senlun service identity already exists or is invalid")
	}
	executor := PackageExecutor{StateDirectory: e.packageDirectory()}
	receipt, err := executor.ReadResources(task.ApplicationID)
	if err != nil || receipt.AppKey != oldKey || receipt.Runtime != "docker" || receipt.State != "ready" || receipt.TaskID != previous.InstanceID || receipt.PackageVersion != previous.Version || receipt.PackageRevision != previous.Manifest.PackageRevision {
		return result, errors.New("agent: original Senlun service ownership requires review")
	}
	rawReceipt, err := os.ReadFile(executor.receiptPath(task.ApplicationID))
	if err != nil {
		return result, err
	}
	digest := sha256.Sum256(rawReceipt)
	if hex.EncodeToString(digest[:]) != task.SourceReceiptSHA256 {
		return result, errors.New("agent: original Senlun service receipt changed")
	}
	original, err := catalog.CanonicalAppManifest(previous.Manifest)
	if err != nil {
		return result, err
	}
	rawOriginal, err := json.Marshal(original)
	if err != nil {
		return result, err
	}
	originalSHA := sha256.Sum256(rawOriginal)
	if receipt.ManifestSHA256 != hex.EncodeToString(originalSHA[:]) {
		return result, errors.New("agent: original Senlun service recipe does not match its receipt")
	}
	var sourceContainer, sourceVolume *RuntimeResource
	for i := range receipt.Resources {
		r := &receipt.Resources[i]
		switch r.Kind {
		case "container":
			if sourceContainer != nil || r.LogicalName != "pulse" {
				return result, errors.New("agent: unexpected Senlun service container")
			}
			sourceContainer = r
		case "volume":
			if sourceVolume != nil || r.LogicalName != "data" || !r.Persistent {
				return result, errors.New("agent: unexpected Senlun service data volume")
			}
			sourceVolume = r
		case "retained-volume":
			// A historical retained volume remains untouched and is still
			// recorded by the original authenticated task result.
		default:
			return result, errors.New("agent: unexpected Senlun service resource requires review")
		}
	}
	if sourceContainer == nil || sourceVolume == nil || sourceContainer.ID == "" || sourceContainer.Name == "" || sourceVolume.Path != "/var/lib/senlun" {
		return result, errors.New("agent: Senlun service resources are incomplete")
	}
	docker, closeDocker, err := e.senlunIdentityDocker()
	if err != nil {
		return result, err
	}
	defer closeDocker()
	if _, ok := docker.(senlunContainerLister); !ok {
		return result, errors.New("agent: Senlun volume writer inspection is unavailable")
	}
	backend := &DockerPackageBackend{Docker: docker, StateDirectory: e.packageDirectory()}
	if configured, ok := e.PackageBackend.(*DockerPackageBackend); ok {
		backend.CheckHealth = configured.CheckHealth
	}
	oldTask := DeploymentTask{ID: task.ID, ApplicationID: task.ApplicationID, AppKey: oldKey, Operation: "upgrade", Manifest: original}
	if err := backend.Inspect(ctx, oldTask, receipt); err != nil {
		return result, err
	}
	current, err := docker.ContainerInspect(ctx, sourceContainer.ID, client.ContainerInspectOptions{})
	if err != nil || current.Container.Config == nil || current.Container.HostConfig == nil || current.Container.State == nil || !current.Container.State.Running || current.Container.ID != sourceContainer.ID || len(task.Manifest.Images) != 1 || current.Container.Config.Image != task.Manifest.Images[0].Reference || !senulunCanonicalEnvironment(current.Container.Config.Env) {
		return result, errors.New("agent: running Senlun service does not match the verified recipe")
	}
	host := current.Container.HostConfig
	if host.NetworkMode != "bridge" || host.AutoRemove || len(host.Mounts) != 1 || len(host.Binds) != 0 || len(host.VolumesFrom) != 0 || host.Mounts[0].Type != mount.TypeVolume || host.Mounts[0].Source != sourceVolume.Name || host.Mounts[0].Target != sourceVolume.Path {
		return result, errors.New("agent: Senlun service mounts require explicit review")
	}
	mounted := 0
	for _, item := range current.Container.Mounts {
		if item.Type == "volume" && item.Name == sourceVolume.Name && item.Destination == sourceVolume.Path {
			mounted++
		}
	}
	if mounted != 1 || len(current.Container.Mounts) != 1 {
		return result, errors.New("agent: active Senlun data mount differs from its receipt")
	}
	_, imageDigest, ok := strings.Cut(current.Container.Config.Image, "@sha256:")
	if !ok || imageDigest == "" {
		return result, errors.New("agent: Senlun image digest is unavailable")
	}
	oldPaths := slices.Clone(receipt.ConfigurationPaths["pulse"])
	for name, paths := range receipt.ConfigurationPaths {
		if name != "pulse" && len(paths) != 0 {
			return result, errors.New("agent: unexpected Senlun configuration component")
		}
	}
	declaration, err := configurationDeclaration(oldPaths)
	if err != nil {
		return result, err
	}
	suffix := strings.TrimPrefix(packageIdentity(task.ID), "vastora-pkg-")
	newVolume := "vastora-senlun-data-" + suffix
	oldContainerName := sourceContainer.Name + "-identity-" + suffix
	helperName := newVolume + "-transfer"
	if _, err := docker.VolumeInspect(ctx, newVolume, client.VolumeInspectOptions{}); !errdefs.IsNotFound(err) {
		return result, errors.New("agent: Senlun target volume already exists or cannot be inspected")
	}
	for _, name := range []string{oldContainerName, helperName} {
		if _, err := docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
			return result, errors.New("agent: Senlun transfer container name is unavailable")
		}
	}
	marker := filepath.Join(filepath.Dir(executor.receiptPath(task.ApplicationID)), "senlun-identity.pending")
	intent := senlunIdentityIntent{ApplicationID: task.ApplicationID, TaskID: task.ID, SourceSHA: task.SourceReceiptSHA256, TargetSHA: task.ManifestSHA256, SourceContainerID: sourceContainer.ID, SourceContainerName: oldContainerName, SourceVolume: sourceVolume.Name, SourceVolumeComponent: sourceVolume.Component}
	intentRaw, err := json.Marshal(intent)
	if err != nil {
		return result, err
	}
	file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return result, errors.New("agent: unfinished Senlun identity migration requires review")
	}
	sourceStopped := false
	targetStartAttempted := false
	defer func() {
		if resultErr != nil {
			// The original can be restarted only while the new writer has
			// definitely never been started. Once a start is attempted, even
			// a lost Docker response requires review instead of rollback.
			if sourceStopped && !targetStartAttempted {
				restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				restoreErr := restoreSenlunBeforeTargetStart(restoreCtx, docker, task, sourceContainer.ID, sourceContainer.Name, oldContainerName, helperName, newVolume)
				cancel()
				resultErr = errors.Join(resultErr, restoreErr)
			}
			resultErr = uncertainTaskOutcome(resultErr)
		}
	}()
	_, writeErr := file.Write(intentRaw)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil || syncSenlunIdentityDirectory(filepath.Dir(marker)) != nil {
		return result, errors.New("agent: Senlun service migration intent requires review")
	}
	timeout := 30
	if _, err := docker.ContainerStop(ctx, sourceContainer.ID, client.ContainerStopOptions{Timeout: &timeout}); err != nil {
		return result, errors.New("agent: Senlun service could not stop for identity transfer")
	}
	sourceStopped = true
	if err := noSenlunVolumeWriters(ctx, docker, sourceVolume.Name); err != nil {
		return result, err
	}
	preserved, err := captureContainerConfiguration(ctx, docker, sourceContainer.ID, declaration, nil)
	if err != nil {
		return result, errors.New("agent: existing Senlun configuration could not be preserved")
	}
	defer clear(preserved)
	if err := ensureOwnedApplicationVolume(ctx, docker, newVolume, newKey, applicationVolumeComponent(newVolume), task.ApplicationID); err != nil {
		return result, err
	}
	helperOptions := client.ContainerCreateOptions{Name: helperName, Config: &container.Config{Image: current.Container.Config.Image, User: current.Container.Config.User, Labels: applicationResourceLabels(newKey, "senlun-transfer", task.ApplicationID, task.ID)}, HostConfig: &container.HostConfig{NetworkMode: "none", Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: newVolume, Target: sourceVolume.Path, VolumeOptions: &mount.VolumeOptions{NoCopy: true}}}}}
	helper, err := docker.ContainerCreate(ctx, helperOptions)
	if err != nil {
		return result, errors.New("agent: Senlun transfer container could not be created")
	}
	if err := transferSenlunVolume(ctx, docker, sourceContainer.ID, helper.ID, sourceVolume.Path); err != nil {
		return result, err
	}
	if _, err := docker.ContainerRemove(ctx, helper.ID, client.ContainerRemoveOptions{}); err != nil {
		return result, errors.New("agent: Senlun transfer container could not be removed")
	}
	if _, err := docker.ContainerRename(ctx, sourceContainer.ID, client.ContainerRenameOptions{NewName: oldContainerName}); err != nil {
		return result, errors.New("agent: original Senlun service could not be staged")
	}
	newConfig := *current.Container.Config
	newConfig.Labels = make(map[string]string, len(current.Container.Config.Labels))
	for key, value := range current.Container.Config.Labels {
		newConfig.Labels[key] = value
	}
	for key, value := range applicationResourceLabels(newKey, "senlun", task.ApplicationID, task.ID) {
		newConfig.Labels[key] = value
	}
	newHost := *host
	newHost.Mounts = slices.Clone(host.Mounts)
	newHost.Mounts[0].Source = newVolume
	newHost.Mounts[0].VolumeOptions = &mount.VolumeOptions{NoCopy: true}
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: sourceContainer.Name, Config: &newConfig, HostConfig: &newHost})
	if err != nil {
		return result, errors.New("agent: renamed Senlun service could not be created")
	}
	if len(preserved) != 0 {
		if _, err := docker.CopyToContainer(ctx, created.ID, client.CopyToContainerOptions{DestinationPath: "/", Content: bytes.NewReader(preserved)}); err != nil {
			return result, errors.New("agent: Senlun configuration could not be restored into the renamed service")
		}
	}
	targetStartAttempted = true
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return result, errors.New("agent: renamed Senlun service could not start")
	}
	canonicalTarget, err := catalog.CanonicalAppManifest(task.Manifest)
	if err != nil {
		return result, err
	}
	newReceipt := *receipt
	newReceipt.AppKey, newReceipt.TaskID, newReceipt.PackageRevision, newReceipt.ManifestSHA256 = newKey, task.ID, task.PackageRevision, task.ManifestSHA256
	newReceipt.Resources = []RuntimeResource{
		{Kind: "volume", LogicalName: "data", Name: newVolume, Path: sourceVolume.Path, Component: applicationVolumeComponent(newVolume), Persistent: true},
		{Kind: "container", LogicalName: "senlun", Name: sourceContainer.Name, ID: created.ID, Component: "senlun", SHA256: imageDigest},
	}
	newReceipt.ConfigurationPaths = map[string][]string{"senlun": oldPaths}
	plan := packageContainerPlan{logical: "senlun", services: task.Manifest.Services}
	if err := backend.checkPlan(ctx, task, &newReceipt, plan); err != nil {
		return result, err
	}
	if err := executor.save(&newReceipt); err != nil || syncSenlunIdentityDirectory(filepath.Dir(marker)) != nil {
		return result, errors.New("agent: renamed Senlun service receipt requires review")
	}
	if err := e.Store.renameSenlunApplied(ctx, previous, task.ID, canonicalTarget); err != nil {
		return result, errors.New("agent: renamed Senlun service registration requires review")
	}
	result.Resources = &newReceipt
	result.SourceReceiptSHA256 = task.SourceReceiptSHA256
	// Keep the original ownership receipt as authenticated task evidence. It
	// contains resource metadata, never the application's data or credentials.
	result.SourceResources = json.RawMessage(rawReceipt)
	address := task.ServiceAddress
	if address == "" {
		address = "127.0.0.1"
	}
	for _, service := range task.Manifest.Services {
		port, err := serviceHostPort(task.Config, service)
		if err != nil {
			return result, err
		}
		result.Services = append(result.Services, ApplicationServiceResult{Name: service.Name, Protocol: service.Protocol, ContainerPort: service.ContainerPort, HostPort: port, Address: address})
	}
	return result, nil
}

type senlunContainerLister interface {
	ContainerList(context.Context, client.ContainerListOptions) (client.ContainerListResult, error)
}

// The source must have no other running Docker writers during its transfer.
func noSenlunVolumeWriters(ctx context.Context, docker packageDockerEngine, volumeName string) error {
	lister, ok := docker.(senlunContainerLister)
	if !ok {
		return errors.New("agent: Senlun volume writer inspection is unavailable")
	}
	running, err := lister.ContainerList(ctx, client.ContainerListOptions{})
	if err != nil {
		return errors.New("agent: Senlun volume writers could not be inspected")
	}
	for _, current := range running.Items {
		for _, item := range current.Mounts {
			if item.Type == mount.TypeVolume && item.Name == volumeName {
				return errors.New("agent: another running container uses the Senlun source volume")
			}
		}
	}
	return nil
}

// restoreSenlunBeforeTargetStart is only called before any attempt to start
// the target. The task remains fenced for review even after the old writer is
// restored. It never touches an unknown container or a historical volume.
func restoreSenlunBeforeTargetStart(ctx context.Context, docker packageDockerEngine, task DeploymentTask, sourceID, sourceName, stagedName, helperName, newVolume string) error {
	source, err := docker.ContainerInspect(ctx, sourceID, client.ContainerInspectOptions{})
	if err != nil || source.Container.ID != sourceID || source.Container.State == nil || source.Container.State.Running || source.Container.Config == nil || validateApplicationResourceLabels(source.Container.Config.Labels, "vastora-official/pulse", "pulse", task.ApplicationID, anyApplicationDeployment) != nil {
		return errors.New("agent: original Senlun service requires manual recovery")
	}
	name := strings.TrimPrefix(source.Container.Name, "/")
	if name != sourceName && name != stagedName {
		return errors.New("agent: original Senlun service name changed during recovery")
	}
	for _, candidate := range []struct{ name, component string }{{helperName, "senlun-transfer"}, {sourceName, "senlun"}} {
		if candidate.name == sourceName && name == sourceName {
			continue
		}
		current, exists, err := inspectOwnedApplicationContainer(ctx, docker, candidate.name, "vastora-official/senlun", candidate.component, task.ApplicationID, task.ID)
		if err != nil || exists && (current.Container.State == nil || current.Container.State.Running) {
			return errors.New("agent: target Senlun service requires manual recovery")
		}
		if exists {
			if _, err := docker.ContainerRemove(ctx, current.Container.ID, client.ContainerRemoveOptions{}); err != nil {
				return errors.New("agent: target Senlun container could not be removed")
			}
		}
	}
	if name == stagedName {
		if _, err := docker.ContainerRename(ctx, sourceID, client.ContainerRenameOptions{NewName: sourceName}); err != nil {
			return errors.New("agent: original Senlun service name could not be restored")
		}
	}
	if _, err := docker.ContainerStart(ctx, sourceID, client.ContainerStartOptions{}); err != nil {
		return errors.New("agent: original Senlun service could not be restarted")
	}
	volume, exists, err := inspectOwnedApplicationVolume(ctx, docker, newVolume, "vastora-official/senlun", applicationVolumeComponent(newVolume), task.ApplicationID)
	if err != nil {
		return errors.New("agent: unused Senlun target volume requires review")
	}
	if exists {
		if _, err := docker.VolumeRemove(ctx, volume.Volume.Name, client.VolumeRemoveOptions{}); err != nil {
			return errors.New("agent: unused Senlun target volume requires review")
		}
	}
	return nil
}

// FinishSenlunServiceIdentity retires only the exact stopped source after the
// Center has committed the authenticated result. Historical retained volumes
// are not touched. A volume still in use by any container is never forced.
func (e ApplicationExecutor) FinishSenlunServiceIdentity(ctx context.Context, task DeploymentTask) error {
	if e.Store == nil || !senlunServiceIdentityTask(task) {
		return errors.New("agent: Senlun service identity cleanup is not authorized")
	}
	path, err := e.Store.validateSenlunIdentityIntent(task)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	var intent senlunIdentityIntent
	if err != nil || json.Unmarshal(raw, &intent) != nil || intent.SourceContainerID == "" || intent.SourceContainerName == "" || intent.SourceVolume == "" || intent.SourceVolumeComponent != applicationVolumeComponent(intent.SourceVolume) {
		return errors.New("agent: Senlun source cleanup evidence is invalid")
	}
	docker, closeDocker, err := e.senlunIdentityDocker()
	if err != nil {
		return err
	}
	defer closeDocker()
	current, exists, err := inspectOwnedApplicationContainer(ctx, docker, intent.SourceContainerName, "vastora-official/pulse", "pulse", task.ApplicationID, anyApplicationDeployment)
	if err != nil || exists && (current.Container.ID != intent.SourceContainerID || current.Container.State == nil || current.Container.State.Running) {
		return errors.New("agent: original Senlun container changed before cleanup")
	}
	if exists {
		if _, err := docker.ContainerRemove(ctx, intent.SourceContainerID, client.ContainerRemoveOptions{}); err != nil {
			return errors.New("agent: original Senlun container could not be retired")
		}
	}
	volume, exists, err := inspectOwnedApplicationVolume(ctx, docker, intent.SourceVolume, "vastora-official/pulse", intent.SourceVolumeComponent, task.ApplicationID)
	if err != nil {
		return err
	}
	if exists {
		if _, err := docker.VolumeRemove(ctx, volume.Volume.Name, client.VolumeRemoveOptions{}); err != nil {
			return errors.New("agent: original Senlun data volume remains in use")
		}
	}
	return e.Store.finishSenlunIdentity(task)
}
