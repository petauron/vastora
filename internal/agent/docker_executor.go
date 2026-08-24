package agent

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

const (
	threeXUIKey            = "vastora-official/3x-ui"
	threeXUIContainer      = "vastora-3x-ui"
	threeXUIDatabaseVolume = "vastora-3x-ui-db"
	cpaKey                 = "vastora-official/cpa"
	cpaContainer           = "vastora-cpa"
	cpaNetwork             = "vastora-cpa-network"
	keeperKey              = "vastora-official/keeper"
	keeperContainer        = "vastora-cpa-usage-keeper"
	komariKey              = "vastora-official/komari-agent"
	komariContainer        = "vastora-komari-agent"
)

type DockerExecutor struct {
	Socket string
}

func (e DockerExecutor) Deploy(ctx context.Context, task DeploymentTask) (ApplicationTaskResult, error) {
	socket := e.Socket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return ApplicationTaskResult{}, fmt.Errorf("agent: connect Docker: %w", err)
	}
	defer docker.Close()
	if task.Operation == "uninstall" {
		return ApplicationTaskResult{}, uninstallApp(ctx, docker, task.AppKey, task.DeleteData)
	}
	bindAddress := "127.0.0.1"
	if task.ServiceAddress != "" {
		if net.ParseIP(task.ServiceAddress) == nil {
			return ApplicationTaskResult{}, errors.New("agent: application requires a valid private service address")
		}
		bindAddress = task.ServiceAddress
	}
	var deployErr error
	generatedSecrets := map[string]string{}
	switch task.AppKey {
	case threeXUIKey:
		var apiToken string
		apiToken, deployErr = deployThreeXUI(ctx, docker, task, bindAddress)
		if deployErr == nil {
			generatedSecrets["api_token"] = apiToken
		}
	case cpaKey:
		deployErr = deployCPA(ctx, docker, task, bindAddress)
	case keeperKey:
		deployErr = deployKeeper(ctx, docker, task, bindAddress)
	case komariKey:
		deployErr = deployKomari(ctx, docker, task)
	default:
		return ApplicationTaskResult{}, errors.New("agent: unsupported official app package")
	}
	if deployErr != nil {
		return ApplicationTaskResult{}, deployErr
	}
	result, err := reportedServices(ctx, task, bindAddress)
	if err != nil {
		if task.AppKey == threeXUIKey {
			return ApplicationTaskResult{GeneratedSecrets: generatedSecrets}, deferTaskUntilReconciled(err)
		}
		return ApplicationTaskResult{}, err
	}
	result.GeneratedSecrets = generatedSecrets
	return result, nil
}

// Maintain cleans committed rollback artifacts and restores an interrupted
// replacement transaction whose canonical container is absent.
func (e DockerExecutor) Maintain(ctx context.Context) error {
	socket := e.Socket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return fmt.Errorf("agent: connect Docker for maintenance: %w", err)
	}
	defer docker.Close()
	return maintainThreeXUIContainers(ctx, docker)
}

type appUninstallEngine interface {
	ContainerRemove(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	VolumeRemove(context.Context, string, client.VolumeRemoveOptions) (client.VolumeRemoveResult, error)
}

func uninstallApp(ctx context.Context, docker appUninstallEngine, appKey string, deleteData bool) error {
	containers := map[string]string{threeXUIKey: threeXUIContainer, cpaKey: cpaContainer, keeperKey: keeperContainer, komariKey: komariContainer}
	name, ok := containers[appKey]
	if !ok {
		return errors.New("agent: unsupported app package")
	}
	containerNames := []string{name}
	if appKey == threeXUIKey {
		if !deleteData {
			transactionalDocker, ok := docker.(threeXUIContainerEngine)
			if !ok {
				return errors.New("agent: Docker engine cannot preserve 3x-ui data before uninstall")
			}
			// Keep-data uninstall never starts a stopped service. It only quiesces
			// the authoritative container and restores a durable rollback snapshot
			// when that is the only safe copy of the retained database.
			if err := prepareThreeXUIKeepDataUninstall(ctx, transactionalDocker); err != nil {
				return fmt.Errorf("agent: preserve 3x-ui data before uninstall: %w", err)
			}
		}
		// Delete-data uninstall is intentionally independent of rollback state:
		// corrupt snapshots must not block an explicit destructive uninstall.
		containerNames = []string{threeXUICandidateContainer, threeXUIBackupContainer, threeXUICleanupContainer, threeXUIContainer}
	}
	for _, containerName := range containerNames {
		if _, err := docker.ContainerRemove(ctx, containerName, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("agent: remove %s container: %w", appKey, err)
		}
	}
	if !deleteData {
		return nil
	}
	volumes := map[string][]string{threeXUIKey: {threeXUIDatabaseVolume, "vastora-3x-ui-cert", "vastora-3x-ui-acme"}, cpaKey: {"vastora-cpa-auths", "vastora-cpa-logs", "vastora-cpa-plugins"}, keeperKey: {"vastora-cpa-keeper-data"}}
	for _, volume := range volumes[appKey] {
		if _, err := docker.VolumeRemove(ctx, volume, client.VolumeRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("agent: remove %s data volume: %w", appKey, err)
		}
	}
	return nil
}
