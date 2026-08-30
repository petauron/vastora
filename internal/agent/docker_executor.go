package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/dockerruntime"
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
	komariContainer              = "vastora-komari-agent"
	applicationDeploymentIDLabel = "io.vastora.application.deployment-id"
)

type HostApplicationManager interface {
	ApplyKomari(context.Context, DeploymentTask) error
	RemoveKomari(context.Context) error
}

type HostApplicationRestorer interface {
	RestoreKomari(context.Context, DeploymentTask) error
}

type ApplicationExecutor struct {
	DockerSocket string
	Host         HostApplicationManager
}

func (e ApplicationExecutor) Deploy(ctx context.Context, task DeploymentTask) (ApplicationTaskResult, error) {
	if err := validateApplicationTask(task); err != nil {
		return ApplicationTaskResult{}, err
	}
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
		return ApplicationTaskResult{}, uninstallDockerApp(ctx, docker, task.AppKey, task.DeleteData)
	}
	if task.OfflineRestore && task.AppKey == threeXUIKey {
		if _, exists, err := inspectThreeXUIVolume(ctx, docker, threeXUIDatabaseVolume); err != nil {
			return ApplicationTaskResult{}, fmt.Errorf("agent: inspect retained 3x-ui state: %w", err)
		} else if !exists {
			return ApplicationTaskResult{}, errors.New("agent: offline restore requires the retained 3x-ui database volume")
		}
	}
	if err := dockerruntime.EnsureNetwork(ctx, docker); err != nil {
		return ApplicationTaskResult{}, err
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

func validateApplicationTask(task DeploymentTask) error {
	if strings.TrimSpace(task.ID) == "" || strings.TrimSpace(task.AppKey) == "" {
		return errors.New("agent: application task identity is required")
	}
	if task.Operation != "install" && task.Operation != "upgrade" && task.Operation != "configure" && task.Operation != "uninstall" {
		return errors.New("agent: unsupported application operation")
	}
	supported := task.AppKey == threeXUIKey || task.AppKey == cpaKey || task.AppKey == keeperKey || task.AppKey == komariKey
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
	if task.AppKey != threeXUIKey && task.ApplicationRole != "" {
		return errors.New("agent: application topology role is only valid for 3x-ui")
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
	case threeXUIKey:
		if task.Manifest.Version != "3.6.0" {
			return errors.New("agent: unsupported official 3x-ui package")
		}
		config, err := decodeThreeXUIConfig(task.Config)
		if err != nil {
			return err
		}
		if _, err := decodeThreeXUISecrets(task.Secrets); err != nil {
			return err
		}
		if _, _, err := threeXUIPorts(bindAddress, config.PanelPort, task.ApplicationRole); err != nil {
			return err
		}
	case cpaKey:
		if task.Manifest.Version != "7.2.128" {
			return errors.New("agent: unsupported official CPA package")
		}
		if _, _, err := decodeCPAConfig(task.Config, task.Secrets); err != nil {
			return err
		}
	case keeperKey:
		if task.Manifest.Version != "1.14.1" {
			return errors.New("agent: unsupported Keeper package")
		}
		if _, _, err := decodeKeeperConfig(task.Config, task.Secrets); err != nil {
			return err
		}
	case komariKey:
		if task.Manifest.Version != "1.2.60" {
			return errors.New("agent: unsupported Komari Agent package")
		}
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

// Restore starts the encrypted last-known-good application state before the
// Agent claims new Center work. It never pulls images or downloads artifacts:
// offline recovery is limited to digest-pinned images and managed host files
// that are already present on this machine.
func (e ApplicationExecutor) Restore(ctx context.Context, store *Store) error {
	installations, err := store.RestorableInstallations(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, installation := range installations {
		task := DeploymentTask{
			Kind: "application.apply", ID: installation.InstanceID, Attempt: 1,
			AppKey: installation.AppKey, Manifest: installation.Manifest,
			Config: installation.Config, Secrets: installation.Secrets,
			Operation: "install", ServiceAddress: installation.ServiceAddress,
			ApplicationRole: installation.ApplicationRole, OfflineRestore: true,
		}
		if installation.Manifest.ID == "" {
			failures = append(failures, fmt.Errorf("agent: legacy %s state cannot be restored offline; reconcile it with Center", installation.AppKey))
			continue
		}
		if installation.AppKey == komariKey {
			restorer, ok := e.Host.(HostApplicationRestorer)
			if !ok {
				failures = append(failures, errors.New("agent: host application recovery capability is not configured"))
				continue
			}
			if err := restorer.RestoreKomari(ctx, task); err != nil {
				failures = append(failures, fmt.Errorf("agent: restore %s: %w", installation.AppKey, err))
			}
			continue
		}
		containerName, ok := map[string]string{threeXUIKey: threeXUIContainer, cpaKey: cpaContainer, keeperKey: keeperContainer}[installation.AppKey]
		if !ok {
			failures = append(failures, fmt.Errorf("agent: persisted application %s is unsupported", installation.AppKey))
			continue
		}
		running, err := e.containerMatchesInstallation(ctx, containerName, installation)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if running {
			bindAddress := "127.0.0.1"
			if installation.ServiceAddress != "" {
				bindAddress = installation.ServiceAddress
			}
			if _, err := reportedServices(ctx, task, bindAddress); err != nil {
				failures = append(failures, fmt.Errorf("agent: verify restored %s health: %w", installation.AppKey, err))
			}
			continue
		}
		if _, err := e.Deploy(ctx, task); err != nil {
			failures = append(failures, fmt.Errorf("agent: restore %s: %w", installation.AppKey, err))
		}
	}
	return errors.Join(failures...)
}

func (e ApplicationExecutor) containerMatchesInstallation(ctx context.Context, containerName string, installation AppliedInstallation) (bool, error) {
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return false, fmt.Errorf("agent: connect Docker for offline restore: %w", err)
	}
	defer docker.Close()
	inspection, err := docker.ContainerInspect(ctx, containerName, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("agent: inspect %s for offline restore: %w", installation.AppKey, err)
	}
	if inspection.Container.State == nil || !inspection.Container.State.Running {
		return false, nil
	}
	if installation.Manifest.ID == "" {
		return true, nil
	}
	label := applicationDeploymentIDLabel
	if installation.AppKey == threeXUIKey {
		label = threeXUIDeploymentIDLabel
	}
	if inspection.Container.Config == nil || inspection.Container.Config.Labels[label] != installation.InstanceID {
		return false, nil
	}
	imageName := map[string]string{threeXUIKey: "3x-ui", cpaKey: "cli-proxy-api", keeperKey: "keeper"}[installation.AppKey]
	expectedImage, err := declaredImage(installation.Manifest, imageName)
	if err != nil {
		return false, err
	}
	return inspection.Container.Config.Image == expectedImage, nil
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
