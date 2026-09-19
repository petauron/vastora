package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/pulse"
)

const (
	threeXUIKey                  = "vastora-official/3x-ui"
	threeXUIContainer            = "vastora-3x-ui"
	threeXUIDatabaseVolume       = "vastora-3x-ui-db"
	cpaKey                       = "vastora-official/cpa"
	cpaContainer                 = "vastora-cpa"
	cpaNetwork                   = "vastora-cpa-network"
	keeperKey                    = "vastora-official/keeper"
	keeperContainer              = "vastora-cpa-usage-keeper"
	komariKey                    = "vastora-official/komari-agent"
	applicationDeploymentIDLabel = "io.vastora.application.deployment-id"
)

var applicationVolumes = map[string][]string{
	threeXUIKey:      {threeXUIDatabaseVolume, "vastora-3x-ui-cert", "vastora-3x-ui-acme"},
	cpaKey:           {"vastora-cpa-auths", "vastora-cpa-logs", "vastora-cpa-plugins"},
	keeperKey:        {"vastora-cpa-keeper-data"},
	pulse.ServiceKey: {"vastora-pulse-data"},
}

type HostApplicationManager interface {
	ApplyKomari(context.Context, DeploymentTask) error
	RemoveKomari(context.Context) error
}

type ApplicationExecutor struct {
	DockerSocket string
	Host         HostApplicationManager
	Store        *Store
}

func (e ApplicationExecutor) Deploy(ctx context.Context, task DeploymentTask) (ApplicationTaskResult, error) {
	if err := validateApplicationTask(task); err != nil {
		return ApplicationTaskResult{}, err
	}
	if task.AppKey == pulse.AgentKey {
		host, ok := e.Host.(interface {
			ApplyPulse(context.Context, DeploymentTask) (ApplicationTaskResult, error)
			RemovePulse(context.Context, string, bool) error
		})
		if !ok {
			return ApplicationTaskResult{}, errors.New("agent: Pulse host capability is not configured")
		}
		if task.Operation == "uninstall" {
			return ApplicationTaskResult{}, host.RemovePulse(ctx, task.ApplicationID, task.DeleteData)
		}
		return host.ApplyPulse(ctx, task)
	}
	if task.AppKey == komariKey {
		if e.Host == nil {
			return ApplicationTaskResult{}, errors.New("agent: host application capability is not configured")
		}
		if task.Operation == "uninstall" {
			return ApplicationTaskResult{}, e.Host.RemoveKomari(ctx)
		}
		if err := e.Host.ApplyKomari(ctx, task); err != nil {
			return ApplicationTaskResult{}, err
		}
		return ApplicationTaskResult{}, nil
	}
	bindAddress := "127.0.0.1"
	if task.ServiceAddress != "" {
		bindAddress = task.ServiceAddress
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
		if task.AppKey == threeXUIKey && e.Store != nil {
			// Stop every worker-side writer before removing its runtime. Leaving
			// the reconciler active until after Docker removal creates a race in
			// which it can journal or apply a new revision during uninstall.
			if err := e.Store.stopXrayWorkerAPI(ctx, false); err != nil {
				return ApplicationTaskResult{}, err
			}
		}
		err := uninstallDockerApp(ctx, docker, task.AppKey, task.ApplicationID, task.DeleteData)
		if err == nil && task.AppKey == threeXUIKey && e.Store != nil && task.DeleteData {
			err = e.Store.stopXrayWorkerAPI(ctx, true)
		}
		return ApplicationTaskResult{}, err
	}
	if err := waitForBindAddress(ctx, bindAddress); err != nil {
		return ApplicationTaskResult{}, err
	}
	// Xray-only workers use the host network and must not depend on creation or
	// repair of the shared Docker bridge. Other application runtimes still use
	// that network and retain the existing setup path.
	if task.AppKey != threeXUIKey || task.ApplicationRole != "worker" {
		if err := dockerruntime.EnsureNetwork(ctx, docker); err != nil {
			return ApplicationTaskResult{}, err
		}
	}
	var deployErr error
	generatedSecrets := map[string]string{}
	switch task.AppKey {
	case pulse.ServiceKey:
		deployErr = deployPulse(ctx, docker, task, bindAddress)
	case threeXUIKey:
		var apiToken string
		if task.ApplicationRole == "worker" {
			apiToken, deployErr = deployXrayWorker(ctx, docker, socket, e.Store, task, bindAddress)
		} else {
			hadWorkerState := false
			if e.Store != nil {
				_, stateErr := e.Store.loadXrayWorkerState(ctx)
				hadWorkerState = stateErr == nil
				if hadWorkerState {
					deployErr = e.Store.stopXrayWorkerAPI(ctx, false)
				}
			}
			if deployErr == nil {
				apiToken, deployErr = deployThreeXUI(ctx, docker, task, bindAddress)
			}
			if hadWorkerState && deployErr != nil {
				deployErr = errors.Join(deployErr, e.Store.ResumeXrayWorker(ctx, socket))
			} else if hadWorkerState && deployErr == nil {
				deployErr = e.Store.retireXrayWorkerState(ctx)
			}
		}
		if apiToken != "" {
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
		return ApplicationTaskResult{GeneratedSecrets: generatedSecrets}, deployErr
	}
	result, err := reportedServices(ctx, task, bindAddress)
	if err != nil {
		if task.AppKey == threeXUIKey {
			return ApplicationTaskResult{GeneratedSecrets: generatedSecrets}, uncertainTaskOutcome(err)
		}
		return ApplicationTaskResult{}, err
	}
	result.GeneratedSecrets = generatedSecrets
	return result, nil
}

func validateApplicationTask(task DeploymentTask) error {
	if strings.TrimSpace(task.ID) == "" || strings.TrimSpace(task.ApplicationID) == "" || strings.TrimSpace(task.AppKey) == "" {
		return errors.New("agent: application task identity is required")
	}
	if task.Operation != "install" && task.Operation != "upgrade" && task.Operation != "configure" && task.Operation != "uninstall" {
		return errors.New("agent: unsupported application operation")
	}
	supported := task.AppKey == threeXUIKey || task.AppKey == cpaKey || task.AppKey == keeperKey || task.AppKey == komariKey || task.AppKey == pulse.ServiceKey || task.AppKey == pulse.AgentKey
	if !supported {
		return errors.New("agent: unsupported official app package")
	}
	if task.Operation == "uninstall" {
		return nil
	}
	if task.DeleteData {
		return errors.New("agent: application data deletion is only valid during uninstall")
	}
	if err := catalog.ValidateApp(task.Manifest); err != nil {
		return fmt.Errorf("agent: reject invalid signed application manifest: %w", err)
	}
	if !strings.HasSuffix(task.AppKey, "/"+task.Manifest.ID) {
		return errors.New("agent: application task does not match its signed manifest")
	}
	if err := ValidateOfficialContract(task.Manifest); err != nil {
		return err
	}
	if task.AppKey != threeXUIKey && task.ApplicationRole != "" {
		return errors.New("agent: application topology role is only valid for 3x-ui")
	}
	if task.AppKey == threeXUIKey && task.ApplicationRole != "master" && task.ApplicationRole != "worker" {
		return errors.New("agent: proxy application requires an explicit controller or worker role")
	}
	if task.RegistryCredential != nil {
		if strings.TrimSpace(task.RegistryCredential.Host) == "" || strings.TrimSpace(task.RegistryCredential.Username) == "" || task.RegistryCredential.Password == "" {
			return errors.New("agent: incomplete Registry credential")
		}
		for _, image := range task.Manifest.Images {
			if _, err := declaredImagePullOptions(task, image.Reference); err != nil {
				return errors.New("agent: Registry credential does not match every declared image authority")
			}
		}
		if len(task.Manifest.Images) == 0 {
			return errors.New("agent: Registry credential cannot be used by this application")
		}
	}
	bindAddress := "127.0.0.1"
	if task.ServiceAddress != "" {
		if ip := net.ParseIP(task.ServiceAddress); ip == nil || ip.To4() == nil {
			return errors.New("agent: application requires a valid private service address")
		}
		bindAddress = task.ServiceAddress
	}
	switch task.AppKey {
	case pulse.AgentKey:
		var config pulse.AgentConfig
		if json.Unmarshal(task.Config, &config) != nil {
			return errors.New("agent: invalid Pulse configuration")
		}
		if err := config.Validate(); err != nil {
			return err
		}
	case pulse.ServiceKey:
		if !networking.IsPrivateServiceAddress(bindAddress) {
			return errors.New("agent: Pulse must bind only to a private service address")
		}
		if _, _, err := pulse.DecodeServiceConfig(task.Config, task.Secrets); err != nil {
			return err
		}
	case threeXUIKey:
		config, err := decodeThreeXUIConfig(task.Config)
		if err != nil {
			return err
		}
		if task.ApplicationRole == "master" {
			if _, err := decodeThreeXUISecrets(task.Secrets); err != nil {
				return err
			}
		} else {
			var secrets map[string]string
			if json.Unmarshal(task.Secrets, &secrets) != nil || len(secrets) > 1 {
				return errors.New("agent: invalid Xray worker credentials")
			}
			token := strings.TrimSpace(secrets["api_token"])
			if len(secrets) == 1 && (token == "" || len(token) > 4096) {
				return errors.New("agent: invalid Xray worker credentials")
			}
		}
		if task.ApplicationRole == "worker" {
			if err := validateThreeXUIServiceAddress(bindAddress, config.PanelPort, task.ApplicationRole); err != nil {
				return err
			}
		} else if _, _, err := threeXUIPorts(bindAddress, config.PanelPort, task.ApplicationRole); err != nil {
			return err
		}
	case cpaKey:
		if _, _, err := decodeCPAConfig(task.Config, task.Secrets); err != nil {
			return err
		}
	case keeperKey:
		if _, _, err := decodeKeeperConfig(task.Config, task.Secrets); err != nil {
			return err
		}
	case komariKey:
		var config struct {
			Endpoint string `json:"endpoint"`
		}
		var secrets struct {
			Token string `json:"token"`
		}
		if json.Unmarshal(task.Config, &config) != nil || json.Unmarshal(task.Secrets, &secrets) != nil {
			return errors.New("agent: invalid Komari Agent configuration")
		}
		if _, err := normalizedKomariEndpoint(config.Endpoint); err != nil || strings.TrimSpace(secrets.Token) == "" || len(secrets.Token) > 4096 {
			return errors.New("agent: incomplete Komari Agent configuration")
		}
	}
	return nil
}

type appUninstallEngine interface {
	ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerRemove(context.Context, string, client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	VolumeInspect(context.Context, string, client.VolumeInspectOptions) (client.VolumeInspectResult, error)
	VolumeRemove(context.Context, string, client.VolumeRemoveOptions) (client.VolumeRemoveResult, error)
}

func uninstallDockerApp(ctx context.Context, docker appUninstallEngine, appKey, applicationID string, deleteData bool) error {
	containers := map[string]string{threeXUIKey: threeXUIContainer, cpaKey: cpaContainer, keeperKey: keeperContainer, pulse.ServiceKey: pulseContainer}
	name, ok := containers[appKey]
	if !ok {
		return errors.New("agent: unsupported app package")
	}
	containerNames := []string{name}
	if appKey == threeXUIKey {
		transactionalDocker, ok := docker.(threeXUIContainerEngine)
		if !ok {
			if !deleteData {
				return errors.New("agent: Docker engine cannot preserve 3x-ui data before uninstall")
			}
			return errors.New("agent: Docker engine cannot verify 3x-ui ownership before uninstall")
		}
		if err := validateThreeXUIOwnership(ctx, transactionalDocker, applicationID); err != nil {
			return err
		}
		if !deleteData {
			// Stop volume writers without restoring snapshots or starting services.
			if err := prepareThreeXUIKeepDataUninstall(ctx, transactionalDocker); err != nil {
				return fmt.Errorf("agent: preserve 3x-ui data before uninstall: %w", err)
			}
		}
		// Delete-data uninstall is intentionally independent of rollback state:
		// corrupt snapshots must not block an explicit destructive uninstall.
		containerNames = []string{threeXUICandidateContainer, threeXUIBackupContainer, threeXUICleanupContainer, threeXUIContainer}
	}
	for _, containerName := range containerNames {
		component := strings.TrimPrefix(appKey, "vastora-official/")
		if err := removeOwnedApplicationContainer(ctx, docker, containerName, appKey, component, applicationID, anyApplicationDeployment); err != nil {
			return fmt.Errorf("agent: remove %s container: %w", appKey, err)
		}
	}
	if !deleteData {
		return nil
	}
	for _, volume := range applicationVolumes[appKey] {
		if err := removeOwnedApplicationVolume(ctx, docker, volume, appKey, applicationVolumeComponent(volume), applicationID); err != nil {
			return fmt.Errorf("agent: remove %s data volume: %w", appKey, err)
		}
	}
	return nil
}
