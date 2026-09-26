package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/petauron/vastora/internal/pulse"
)

func historicalReceipt(task DeploymentTask, kind string) *InstanceResources {
	return &InstanceResources{Version: 1, ApplicationID: task.ApplicationID, AppKey: task.AppKey, Runtime: kind, PackageVersion: task.Manifest.Version, PackageRevision: 0, ManifestSHA256: task.ManifestSHA256, State: "ready", TaskID: task.ID}
}

// These are one-shot historical bindings, not an executable app allowlist.
// Installation of all ordinary v4 packages is selected by runtime kind alone.
func historicalDockerResources(ctx context.Context, task DeploymentTask, history AppliedInstallation, docker packageDockerEngine, stateDirectory string) (*InstanceResources, error) {
	names := map[string]struct{ name, logical, component string }{
		pulse.ServiceKey: {pulseContainer, "pulse", "pulse"},
		cpaKey:           {cpaContainer, "cpa", "cpa"},
		keeperKey:        {keeperContainer, "keeper", "keeper"},
		threeXUIKey:      {threeXUIContainer, "3x-ui", "3x-ui"},
		meridianKey:      {meridianXrayContainer, "xray", "xray"},
	}
	binding, ok := names[task.AppKey]
	if !ok {
		return nil, errors.New("agent: no audited historical resource mapping exists")
	}
	if task.AppKey == threeXUIKey && history.ApplicationRole == "worker" {
		binding.name, binding.logical, binding.component = xrayWorkerContainer, "xray", "xray"
	}
	current, exists, err := inspectOwnedApplicationContainer(ctx, docker, binding.name, task.AppKey, binding.component, task.ApplicationID, anyApplicationDeployment)
	if err != nil || !exists {
		return nil, errors.New("agent: historical container ownership could not be verified")
	}
	imageDeclared := false
	for _, image := range history.Manifest.Images {
		if image.Reference == current.Container.Config.Image {
			imageDeclared = true
		}
	}
	if !imageDeclared {
		return nil, errors.New("agent: historical container image is not the installed manifest image")
	}
	receipt := historicalReceipt(task, "docker")
	resource := RuntimeResource{Kind: "container", LogicalName: binding.logical, Name: binding.name, ID: current.Container.ID, Component: binding.component}
	if _, digest, ok := strings.Cut(current.Container.Config.Image, "@sha256:"); ok {
		resource.SHA256 = digest
	}
	if current.Container.State != nil {
		resource.StartedAt = current.Container.State.StartedAt
	}
	receipt.Resources = append(receipt.Resources, resource)
	logicalVolumes := map[string]string{"vastora-pulse-data": "data", "vastora-cpa-auths": "auths", "vastora-cpa-logs": "logs", "vastora-cpa-plugins": "plugins", "vastora-cpa-keeper-data": "data", threeXUIDatabaseVolume: "db", "vastora-3x-ui-cert": "cert", "vastora-3x-ui-acme": "acme"}
	for _, mounted := range current.Container.Mounts {
		if mounted.Type == "volume" {
			logical, known := logicalVolumes[mounted.Name]
			if !known {
				return nil, errors.New("agent: unknown historical volume mapping")
			}
			component := applicationVolumeComponent(mounted.Name)
			_, exists, err := inspectOwnedApplicationVolume(ctx, docker, mounted.Name, task.AppKey, component, task.ApplicationID)
			if err != nil || !exists {
				return nil, errors.New("agent: historical volume ownership could not be verified")
			}
			receipt.Resources = append(receipt.Resources, RuntimeResource{Kind: "volume", LogicalName: logical, Name: mounted.Name, Component: component, Persistent: true, Path: mounted.Destination})
		} else if mounted.Type == "bind" {
			// Only a managed Agent data path is admissible as old host storage.
			if !strings.HasPrefix(mounted.Source, filepath.Clean(stateDirectory)+string(os.PathSeparator)) {
				return nil, errors.New("agent: historical host mount requires manual ownership review")
			}
			if err := checkPackageParents(filepath.Join(mounted.Source, "member")); err != nil {
				return nil, err
			}
			receipt.Resources = append(receipt.Resources, RuntimeResource{Kind: "bind", LogicalName: "legacy-runtime", Name: mounted.Source, Path: mounted.Source, ID: current.Container.ID, Component: mounted.Destination, Persistent: true})
		} else {
			return nil, errors.New("agent: historical mount type requires manual ownership review")
		}
	}
	return receipt, nil
}

func historicalSystemdResources(ctx context.Context, task DeploymentTask, history AppliedInstallation, manager SystemdHostApplicationManager) (*InstanceResources, error) {
	receipt := historicalReceipt(task, "systemd")
	paths := map[string]string{}
	unitPath := komariUnitPath
	if task.AppKey == komariKey {
		paths[komariBinaryPath] = "artifact:komari-agent"
		paths[komariConfigPath] = "config:config.json"
	} else {
		unitPath = pulseUnitPath
		paths[pulseBinary] = "artifact:pulse-agent"
		paths[pulseEnv] = "environment"
		paths[pulseToken] = "credential:enrollment-token"
		paths[pulseDigest] = "legacy-package-proof"
		paths[pulseArchive] = "legacy-release-archive"
	}
	paths[unitPath] = "service"
	for path, logical := range paths {
		actual := manager.path(path)
		if err := checkPackageParents(actual); err != nil {
			return nil, err
		}
		snapshot, err := captureHostFile(actual)
		if err != nil || !snapshot.Exists {
			return nil, errors.New("agent: historical application file is missing or unsafe")
		}
		kind := "file"
		if path == unitPath {
			kind = "unit"
			if !bytes.HasPrefix(snapshot.Data, []byte(komariUnitMarker)) {
				return nil, errors.New("agent: historical unit is not managed")
			}
			if task.AppKey == pulse.AgentKey && !pulseUnitOwnedBy(snapshot.Data, task.ApplicationID) {
				return nil, errors.New("agent: historical unit belongs to another instance")
			}
		}
		digest := sha256.Sum256(snapshot.Data)
		resource := RuntimeResource{Kind: kind, LogicalName: logical, Name: filepath.Base(path), Path: path, SHA256: hex.EncodeToString(digest[:]), Component: "historical-verified"}
		if kind == "unit" {
			observed, err := manager.readPackageUnit(ctx, resource.Name)
			if err != nil {
				return nil, err
			}
			resource.MainPID, resource.InvocationID, resource.StartedAt = observed.MainPID, observed.InvocationID, observed.StartedAt
		}
		receipt.Resources = append(receipt.Resources, resource)
	}
	if task.AppKey == pulse.AgentKey {
		info, err := os.Lstat(manager.path(pulseState))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("agent: historical Pulse state is missing or unsafe")
		}
		receipt.Resources = append(receipt.Resources, RuntimeResource{Kind: "directory", LogicalName: "state", Name: filepath.Base(pulseState), Path: pulseState, Persistent: true, Component: "historical-verified"})
	}
	_ = history // The caller already verified the encrypted historical identity.
	return receipt, nil
}
