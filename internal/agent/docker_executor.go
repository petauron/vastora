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
	threeXUIKey       = "vastora-official/3x-ui"
	threeXUIContainer = "vastora-3x-ui"
	cpaKey            = "vastora-official/cpa"
	cpaContainer      = "vastora-cpa"
	cpaNetwork        = "vastora-cpa-network"
	keeperKey         = "vastora-official/keeper"
	keeperContainer   = "vastora-cpa-usage-keeper"
	komariKey         = "vastora-official/komari-agent"
	komariContainer   = "vastora-komari-agent"
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
		return ApplicationTaskResult{}, err
	}
	result.GeneratedSecrets = generatedSecrets
	return result, nil
}

func uninstallApp(ctx context.Context, docker *client.Client, appKey string, deleteData bool) error {
	containers := map[string]string{threeXUIKey: threeXUIContainer, cpaKey: cpaContainer, keeperKey: keeperContainer, komariKey: komariContainer}
	name, ok := containers[appKey]
	if !ok {
		return errors.New("agent: unsupported app package")
	}
	if _, err := docker.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("agent: remove %s container: %w", appKey, err)
	}
	if !deleteData {
		return nil
	}
	volumes := map[string][]string{threeXUIKey: {"vastora-3x-ui-db", "vastora-3x-ui-cert", "vastora-3x-ui-acme"}, cpaKey: {"vastora-cpa-auths", "vastora-cpa-logs", "vastora-cpa-plugins"}, keeperKey: {"vastora-cpa-keeper-data"}}
	for _, volume := range volumes[appKey] {
		if _, err := docker.VolumeRemove(ctx, volume, client.VolumeRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("agent: remove %s data volume: %w", appKey, err)
		}
	}
	return nil
}
