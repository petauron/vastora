package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
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

type HostApplicationManager interface {
	ApplyKomari(context.Context, DeploymentTask) error
	RemoveKomari(context.Context) error
}

type ApplicationExecutor struct {
	DockerSocket string
	Host         HostApplicationManager
}

func (e ApplicationExecutor) Deploy(ctx context.Context, task DeploymentTask) (ApplicationTaskResult, error) {
	if task.AppKey == komariKey {
		if e.Host == nil {
			return ApplicationTaskResult{}, errors.New("agent: host application capability is not configured")
		}
		if task.Operation == "uninstall" {
			return ApplicationTaskResult{}, errors.Join(e.Host.RemoveKomari(ctx), e.removeLegacyKomariContainer(ctx))
		}
		if err := e.Host.ApplyKomari(ctx, task); err != nil {
			return ApplicationTaskResult{}, err
		}
		if err := e.removeLegacyKomariContainer(ctx); err != nil {
			return ApplicationTaskResult{}, errors.Join(err, e.Host.RemoveKomari(ctx))
		}
		return ApplicationTaskResult{}, nil
	}
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return ApplicationTaskResult{}, fmt.Errorf("agent: connect Docker: %w", err)
	}
	defer docker.Close()
	if task.Operation == "uninstall" {
		return ApplicationTaskResult{}, uninstallDockerApp(ctx, docker, task.AppKey, task.DeleteData)
	}
	if err := dockerruntime.EnsureNetwork(ctx, docker); err != nil {
		return ApplicationTaskResult{}, err
	}
	bindAddress := "127.0.0.1"
	if task.ServiceAddress != "" {
		if ip := net.ParseIP(task.ServiceAddress); ip == nil || ip.To4() == nil {
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
func (e ApplicationExecutor) Maintain(ctx context.Context) error {
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	if path, ok := strings.CutPrefix(socket, "unix://"); ok {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		}
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

func uninstallDockerApp(ctx context.Context, docker appUninstallEngine, appKey string, deleteData bool) error {
	containers := map[string]string{threeXUIKey: threeXUIContainer, cpaKey: cpaContainer, keeperKey: keeperContainer}
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

func (e ApplicationExecutor) removeLegacyKomariContainer(ctx context.Context) error {
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	if path, ok := strings.CutPrefix(socket, "unix://"); ok {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return fmt.Errorf("agent: connect Docker to remove legacy Komari container: %w", err)
	}
	defer docker.Close()
	if _, err := docker.ContainerRemove(ctx, komariContainer, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("agent: remove legacy Komari container: %w", err)
	}
	return nil
}
