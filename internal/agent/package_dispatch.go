package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	"github.com/moby/moby/client"
	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/pulse"
)

func (e ApplicationExecutor) packageDirectory() string {
	directory := e.PackageStateDirectory
	if directory == "" && e.Store != nil {
		directory = e.Store.dataDir
	}
	if directory != "" {
		if canonical, err := filepath.EvalSymlinks(directory); err == nil {
			return canonical
		}
	}
	return directory
}

// Bind the completed integration receipt to the authenticated outer task, not
// its local runtime revision identifier.
func (e ApplicationExecutor) CompleteCommandResources(applicationID, taskID string) (*InstanceResources, error) {
	executor := PackageExecutor{StateDirectory: e.packageDirectory()}
	receipt, err := executor.ReadResources(applicationID)
	if err != nil {
		return nil, err
	}
	if receipt.State != "ready" || taskID == "" {
		return nil, errors.New("agent: integration receipt is not committed")
	}
	receipt.TaskID = taskID
	return receipt, executor.save(receipt)
}

func (e ApplicationExecutor) deployPackage(ctx context.Context, task DeploymentTask) (ApplicationTaskResult, error) {
	if task.Operation == "adopt" {
		return e.Adopt(ctx, task)
	}
	if err := validatePackageTask(task); err != nil {
		return ApplicationTaskResult{}, err
	}
	if e.PackageBackend != nil {
		return (PackageExecutor{StateDirectory: e.packageDirectory(), Backend: e.PackageBackend}).Deploy(ctx, task)
	}
	kind := ""
	if task.Manifest.Runtime != nil {
		kind = task.Manifest.Runtime.Kind
	} else if task.Operation == "uninstall" {
		receipt, err := e.ApplicationResources(task.ApplicationID)
		if err != nil {
			return ApplicationTaskResult{}, err
		}
		kind = receipt.Runtime
	}
	if kind == "systemd" {
		manager, ok := e.Host.(SystemdHostApplicationManager)
		if !ok {
			return ApplicationTaskResult{}, errors.New("agent: generic systemd executor is unavailable")
		}
		backend := &SystemdPackageBackend{Manager: manager, StateDirectory: e.packageDirectory()}
		return (PackageExecutor{StateDirectory: e.packageDirectory(), Backend: backend}).Deploy(ctx, task)
	}
	// Product runtime state is a separate integration. An unsupported integrated
	// migration must fail before touching its runtime, never silently become a
	// plain container deployment with its control-plane state lost.
	if task.Operation != "uninstall" && task.AppKey == threeXUIKey {
		return ApplicationTaskResult{}, errors.New("agent: proxy product integration requires a reviewed v4 runtime migration")
	}
	if kind != "docker" {
		return ApplicationTaskResult{}, errors.New("agent: receipt names unsupported runtime")
	}
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return ApplicationTaskResult{}, errors.New("agent: Docker executor unavailable")
	}
	defer docker.Close()
	backend := &DockerPackageBackend{Docker: docker, StateDirectory: e.packageDirectory()}
	if task.AppKey == meridianKey {
		integrated := &meridianPackageBackend{DockerPackageBackend: backend, Executor: e, Client: docker}
		return (PackageExecutor{StateDirectory: e.packageDirectory(), Backend: integrated}).Deploy(ctx, task)
	}
	return (PackageExecutor{StateDirectory: e.packageDirectory(), Backend: backend}).Deploy(ctx, task)
}

// PackageCapabilities advertises only executors actually configured on this
// process; a bool Docker flag cannot imply knowledge of a future protocol.
func (e ApplicationExecutor) PackageCapabilities(dockerAvailable bool) (map[string]int, []string) {
	versions := map[string]int{}
	if dockerAvailable {
		versions["docker"] = 1
	}
	if _, ok := e.Host.(SystemdHostApplicationManager); ok && runtime.GOOS == "linux" {
		versions["systemd"] = 1
	}
	capabilities := []string{"root", "host-network", "host-path", "devices"}
	if dockerAvailable && e.Store != nil {
		capabilities = append(capabilities, "meridian-runtime")
	}
	return versions, capabilities
}

func (e ApplicationExecutor) Adopt(ctx context.Context, task DeploymentTask) (ApplicationTaskResult, error) {
	if e.Store == nil {
		return ApplicationTaskResult{}, errors.New("agent: adoption requires authenticated historical installation state")
	}
	history, err := e.Store.AppliedInstallation(ctx, task.AppKey)
	if err != nil || history.ApplicationID != task.ApplicationID || history.Manifest.ID != task.Manifest.ID || history.Version != task.Manifest.Version {
		return ApplicationTaskResult{}, errors.New("agent: adoption history does not match the requested instance")
	}
	// No v3 manifest is converted into a forged v4 signed package. Compare the
	// decoded historical object; Center retains and hashes its original bytes.
	local, _ := json.Marshal(history.Manifest)
	remote, _ := json.Marshal(task.Manifest)
	if string(local) != string(remote) {
		return ApplicationTaskResult{}, errors.New("agent: adoption historical manifest mismatch")
	}
	if task.ManifestSHA256 == "" || task.PackageRevision != 0 {
		return ApplicationTaskResult{}, errors.New("agent: adoption requires the original historical manifest digest")
	}
	digest := sha256.Sum256(task.HistoricalManifest)
	var supplied catalog.AppManifest
	if hex.EncodeToString(digest[:]) != task.ManifestSHA256 || json.Unmarshal(task.HistoricalManifest, &supplied) != nil {
		return ApplicationTaskResult{}, errors.New("agent: historical raw manifest digest mismatch")
	}
	decoded, _ := json.Marshal(supplied)
	if string(decoded) != string(local) {
		return ApplicationTaskResult{}, errors.New("agent: historical raw manifest does not match installed evidence")
	}
	task.Operation = "adopt"
	var backend PackageBackend
	var receipt *InstanceResources
	if task.AppKey == pulse.AgentKey || task.AppKey == komariKey {
		manager, ok := e.Host.(SystemdHostApplicationManager)
		if !ok {
			return ApplicationTaskResult{}, errors.New("agent: systemd historical inspection unavailable")
		}
		backend = &SystemdPackageBackend{Manager: manager, StateDirectory: e.packageDirectory()}
		receipt, err = historicalSystemdResources(ctx, task, history, manager)
	} else {
		socket := e.DockerSocket
		if socket == "" {
			socket = "unix:///var/run/docker.sock"
		}
		docker, connectErr := client.New(client.WithHost(socket))
		if connectErr != nil {
			return ApplicationTaskResult{}, connectErr
		}
		defer docker.Close()
		backend = &DockerPackageBackend{Docker: docker, StateDirectory: e.packageDirectory()}
		receipt, err = historicalDockerResources(ctx, task, history, docker, e.packageDirectory())
	}
	if err != nil {
		return ApplicationTaskResult{}, err
	}
	task.Resources = receipt
	return (PackageExecutor{StateDirectory: e.packageDirectory(), Backend: backend}).Deploy(ctx, task)
}

// Resources are read without probing or restarting applications. This is also
// suitable for an operator's migration inventory before opening maintenance.
func (e ApplicationExecutor) ApplicationResources(applicationID string) (*InstanceResources, error) {
	value, err := (PackageExecutor{StateDirectory: e.packageDirectory()}).ReadResources(applicationID)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errApplicationNotInstalled
	}
	return value, err
}
