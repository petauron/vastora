package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
)

func sharedWebsiteConfigure(task DeploymentTask) bool {
	return task.Operation == "configure" && daylightKey(task.AppKey) && task.Manifest.Runtime != nil && (task.Manifest.Runtime.Kind == "native" || task.Manifest.Runtime.Kind == "systemd") && task.Manifest.Runtime.Version == 2 && slices.Contains(task.Manifest.Runtime.RequiredCapabilities, "shared-caddy")
}

func (s *Store) preparedWebsiteBinding(ctx context.Context, id, hostname string) (CaddySite, error) {
	invalid := errors.New("agent: changing a website hostname requires its prepared migration")
	j, err := s.readNodeSiteJournal(ctx, id)
	if err != nil || j.State != "prepared" || j.Task.Action != "prepare-migration" {
		return CaddySite{}, invalid
	}
	var previous, candidate CaddySite
	if json.Unmarshal(j.Previous, &previous) != nil || json.Unmarshal(j.Candidate, &candidate) != nil || previous.ApplicationID != id || candidate.ApplicationID != id || previous.Hostname != j.Task.PreviousHostname || candidate.Hostname != j.Task.Hostname {
		return CaddySite{}, invalid
	}
	if hostname == candidate.Hostname && candidate.validate() == nil {
		return candidate, nil
	}
	if hostname == previous.Hostname && previous.validate() == nil {
		return previous, nil
	}
	return CaddySite{}, invalid
}

func (b *NativeServicesPackageBackend) verifyWebsiteConfiguration(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	invalid := errors.New("agent: website configure may change only its prepared hostname on the ready installed package")
	if b.Store == nil || receipt == nil || receipt.State != "ready" || receipt.ManifestSHA256 != task.ManifestSHA256 || receipt.PackageRevision != task.PackageRevision || receipt.PackageVersion != task.Manifest.Version {
		return invalid
	}
	installed, err := b.Store.AppliedInstallation(ctx, task.AppKey)
	if err != nil || installed.ApplicationID != task.ApplicationID || installed.ServiceAddress != task.ServiceAddress {
		return invalid
	}
	var before, after map[string]any
	if json.Unmarshal(installed.Config, &before) != nil || json.Unmarshal(task.Config, &after) != nil || before == nil || after == nil {
		return invalid
	}
	old, oldOK := before["hostname"].(string)
	next, nextOK := after["hostname"].(string)
	if !oldOK || !nextOK || old == next {
		return invalid
	}
	delete(before, "hostname")
	delete(after, "hostname")
	if !reflect.DeepEqual(before, after) {
		return invalid
	}
	var oldSecrets, newSecrets any
	if len(installed.Secrets) == 0 {
		installed.Secrets = json.RawMessage(`{}`)
	}
	if len(task.Secrets) == 0 {
		task.Secrets = json.RawMessage(`{}`)
	}
	if json.Unmarshal(installed.Secrets, &oldSecrets) != nil || json.Unmarshal(task.Secrets, &newSecrets) != nil || !reflect.DeepEqual(oldSecrets, newSecrets) {
		return invalid
	}
	if _, err = b.Store.preparedWebsiteBinding(ctx, task.ApplicationID, old); err != nil {
		return err
	}
	_, err = b.Store.preparedWebsiteBinding(ctx, task.ApplicationID, next)
	return err
}
