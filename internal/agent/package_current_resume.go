package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// This is a read-only proof of the already completed original package. It never
// installs, adopts, repairs a receipt or falls back to a fresh installation.
// The outer task may then finish its separately validated retained certificate.
func (e PackageExecutor) resumeCurrentSite(ctx context.Context, task DeploymentTask, receipt *InstanceResources) (ApplicationTaskResult, error) {
	result := ApplicationTaskResult{Resources: receipt}
	invalid := errors.New("agent: current site resume requires its exact healthy original installation")
	if task.Kind != "application.apply" || task.Operation != "install" || task.AppKey != "vastora-official/daylight" || task.Attempt != 2 || !strings.HasPrefix(task.ID, "reinstall-current-") || task.NodeSite == nil || task.NodeSite.ApplicationID != task.ApplicationID || task.NodeSite.AppKey != task.AppKey || task.NodeSite.Action != "apply" || task.NodeSite.Validate() != nil || task.Authorization.ID == "" || receipt == nil || receipt.State != "ready" || receipt.TaskID != task.ID || receipt.ManifestSHA256 != task.ManifestSHA256 || receipt.PackageVersion != task.Manifest.Version || receipt.PackageRevision != task.PackageRevision || !slices.Equal(receipt.AuthorizedCapabilities, task.AuthorizedCapabilities) {
		return result, invalid
	}
	backend, ok := e.Backend.(*NativeServicesPackageBackend)
	if !ok || backend.Store == nil {
		return result, invalid
	}
	installed, err := backend.Store.AppliedInstallation(ctx, task.AppKey)
	if err != nil || installed.InstanceID != task.ID || installed.ApplicationID != task.ApplicationID || installed.Version != task.Manifest.Version || string(installed.Config) != string(task.Config) || installed.ServiceAddress != task.ServiceAddress {
		return result, invalid
	}
	oldManifest, _ := json.Marshal(installed.Manifest)
	newManifest, _ := json.Marshal(task.Manifest)
	if string(oldManifest) != string(newManifest) {
		return result, invalid
	}
	if err = backend.Inspect(ctx, task, receipt); err != nil {
		return result, err
	}
	if err = backend.Prepare(ctx, task, receipt); err != nil {
		return result, err
	}
	if err = backend.Healthy(ctx, task, receipt); err != nil {
		return result, err
	}
	for _, service := range task.Manifest.Services {
		port, err := serviceHostPort(task.Config, service)
		if err != nil {
			return result, err
		}
		result.Services = append(result.Services, ApplicationServiceResult{Name: service.Name, Protocol: service.Protocol, ContainerPort: service.ContainerPort, HostPort: port, Address: task.ServiceAddress})
	}
	return result, nil
}
