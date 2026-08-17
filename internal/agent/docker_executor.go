package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/catalog"
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

func deployThreeXUI(ctx context.Context, docker *client.Client, task DeploymentTask, bindAddress string) (string, error) {
	if task.Manifest.ID != "3x-ui" || task.Manifest.Version != "3.6.0" {
		return "", errors.New("agent: unsupported official 3x-ui package")
	}
	imageRef, err := declaredImage(task.Manifest, "3x-ui")
	if err != nil {
		return "", err
	}
	settings, err := decodeThreeXUIConfig(task.Config)
	if err != nil {
		return "", err
	}
	credentials, err := decodeThreeXUISecrets(task.Secrets)
	if err != nil {
		return "", err
	}
	pull, err := docker.ImagePull(ctx, imageRef, client.ImagePullOptions{})
	if err != nil {
		return "", fmt.Errorf("agent: pull 3x-ui image: %w", err)
	}
	_, _ = io.Copy(io.Discard, pull)
	_ = pull.Close()
	_, _ = docker.ContainerRemove(ctx, threeXUIContainer, client.ContainerRemoveOptions{Force: true})

	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: imageRef,
			Tty:   true,
			Env: []string{
				"TZ=" + settings.Timezone,
				"XUI_INIT_WEB_BASE_PATH=/",
				"XUI_SKIP_HSTS=true",
				"XUI_ENABLE_FAIL2BAN=" + strconv.FormatBool(settings.EnableFail2ban),
				"XRAY_VMESS_AEAD_FORCED=" + strconv.FormatBool(settings.VMessAEADForced),
			},
		},
		HostConfig: &container.HostConfig{
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")},
			CapAdd:        []string{"NET_ADMIN", "NET_RAW"},
			NetworkMode:   container.NetworkMode("host"),
			Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: "vastora-3x-ui-db", Target: "/etc/x-ui"},
				{Type: mount.TypeVolume, Source: "vastora-3x-ui-cert", Target: "/root/cert"},
				{Type: mount.TypeVolume, Source: "vastora-3x-ui-acme", Target: "/root/.acme.sh"},
			},
		},
		Name: threeXUIContainer,
	})
	if err != nil {
		return "", fmt.Errorf("agent: create 3x-ui container: %w", err)
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return "", fmt.Errorf("agent: start 3x-ui container: %w", err)
	}
	if err := configureThreeXUI(ctx, docker, created.ID, bindAddress, settings.PanelPort, credentials); err != nil {
		return "", err
	}
	inspected, err := docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("agent: inspect 3x-ui container: %w", err)
	}
	if inspected.Container.State == nil || !inspected.Container.State.Running {
		return "", errors.New("agent: 3x-ui container did not remain running")
	}
	apiToken, err := threeXUIAPIToken(ctx, docker, created.ID)
	if err != nil {
		return "", err
	}
	if err := configureThreeXUISubscription(ctx, bindAddress, settings.PanelPort, apiToken); err != nil {
		return "", err
	}
	return apiToken, nil
}

type threeXUIConfig struct {
	Timezone        string `json:"timezone"`
	PanelPort       int    `json:"panel_port"`
	EnableFail2ban  bool   `json:"enable_fail2ban"`
	VMessAEADForced bool   `json:"vmess_aead_forced"`
}

type threeXUISecrets struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func decodeThreeXUIConfig(raw json.RawMessage) (threeXUIConfig, error) {
	var config threeXUIConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return config, errors.New("agent: invalid 3x-ui configuration")
	}
	if config.Timezone == "" || config.PanelPort < 1024 || config.PanelPort > 65535 {
		return config, errors.New("agent: invalid 3x-ui configuration")
	}
	return config, nil
}

func decodeThreeXUISecrets(raw json.RawMessage) (threeXUISecrets, error) {
	var value threeXUISecrets
	if json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value.Username) == "" || len(value.Password) < 20 {
		return value, errors.New("agent: incomplete 3x-ui credentials")
	}
	return value, nil
}

func configureThreeXUI(ctx context.Context, docker *client.Client, containerID, bindAddress string, panelPort int, credentials threeXUISecrets) error {
	command := []string{"/app/x-ui", "setting", "-webBasePath", "/", "-listenIP", bindAddress, "-port", strconv.Itoa(panelPort), "-username", credentials.Username, "-password", credentials.Password}
	_, err := runContainerCommand(ctx, docker, containerID, command)
	if err != nil {
		return fmt.Errorf("agent: configure 3x-ui: %w", err)
	}
	timeout := 10
	if _, err := docker.ContainerRestart(ctx, containerID, client.ContainerRestartOptions{Timeout: &timeout}); err != nil {
		return fmt.Errorf("agent: restart 3x-ui after configuration: %w", err)
	}
	return waitForEndpoint(ctx, bindAddress, panelPort)
}

func threeXUIAPIToken(ctx context.Context, docker *client.Client, containerID string) (string, error) {
	output, err := runContainerCommand(ctx, docker, containerID, []string{"/app/x-ui", "setting", "-getApiToken", "true"})
	if err != nil {
		return "", fmt.Errorf("agent: create 3x-ui API token: %w", err)
	}
	for _, line := range strings.Split(output, "\n") {
		if value, found := strings.CutPrefix(strings.TrimSpace(line), "apiToken:"); found && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
	}
	return "", errors.New("agent: 3x-ui did not return an API token")
}

func configureThreeXUISubscription(ctx context.Context, address string, panelPort int, apiToken string) error {
	baseURL := "http://" + net.JoinHostPort(address, strconv.Itoa(panelPort))
	settings, err := threeXUIRequest(ctx, http.MethodPost, baseURL+"/panel/api/setting/all", apiToken, map[string]any{})
	if err != nil {
		return fmt.Errorf("agent: read 3x-ui subscription settings: %w", err)
	}
	settings["subListen"] = address
	settings["subPort"] = 2096
	settings["subPath"] = "/sub/"
	if _, err := threeXUIRequest(ctx, http.MethodPost, baseURL+"/panel/api/setting/update", apiToken, settings); err != nil {
		return fmt.Errorf("agent: update 3x-ui subscription settings: %w", err)
	}
	return waitForEndpoint(ctx, address, 2096)
}

func threeXUIRequest(ctx context.Context, method, endpoint, token string, body any) (map[string]any, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var result struct {
		Success bool           `json:"success"`
		Message string         `json:"msg"`
		Object  map[string]any `json:"obj"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&result) != nil || response.StatusCode < 200 || response.StatusCode >= 300 || !result.Success {
		return nil, fmt.Errorf("3x-ui rejected the request: %s", strings.TrimSpace(result.Message))
	}
	if result.Object == nil {
		result.Object = map[string]any{}
	}
	return result.Object, nil
}

func runContainerCommand(ctx context.Context, docker *client.Client, containerID string, command []string) (string, error) {
	created, err := docker.ExecCreate(ctx, containerID, client.ExecCreateOptions{Cmd: command, WorkingDir: "/app", TTY: true, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return "", err
	}
	attached, err := docker.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: true})
	if err != nil {
		return "", err
	}
	defer attached.Close()
	output, readErr := io.ReadAll(io.LimitReader(attached.Reader, 1<<20))
	if readErr != nil {
		return "", readErr
	}
	inspection, err := docker.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return "", err
	}
	if inspection.Running || inspection.ExitCode != 0 {
		return string(output), fmt.Errorf("command exited with status %d", inspection.ExitCode)
	}
	return string(output), nil
}

type cpaConfig struct {
	Timezone string `json:"timezone"`
	Debug    bool   `json:"debug"`
}

type cpaSecrets struct {
	ManagementKey string `json:"management_key"`
	APIKey        string `json:"api_key"`
}

func deployCPA(ctx context.Context, docker *client.Client, task DeploymentTask, bindAddress string) error {
	if task.Manifest.ID != "cpa" || task.Manifest.Version != "7.2.128" {
		return errors.New("agent: unsupported official CPA package")
	}
	imageRef, err := declaredImage(task.Manifest, "cli-proxy-api")
	if err != nil {
		return err
	}
	settings, secrets, err := decodeCPAConfig(task.Config, task.Secrets)
	if err != nil {
		return err
	}
	pull, err := docker.ImagePull(ctx, imageRef, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("agent: pull CPA image: %w", err)
	}
	_, _ = io.Copy(io.Discard, pull)
	_ = pull.Close()
	_, _ = docker.ContainerRemove(ctx, cpaContainer, client.ContainerRemoveOptions{Force: true})
	if err := ensureCPANetwork(ctx, docker); err != nil {
		return err
	}

	port := network.MustParsePort("8317/tcp")
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:        imageRef,
			Env:          []string{"TZ=" + settings.Timezone},
			ExposedPorts: network.PortSet{port: struct{}{}},
			WorkingDir:   "/CLIProxyAPI",
		},
		HostConfig: &container.HostConfig{
			NetworkMode:   container.NetworkMode(cpaNetwork),
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")},
			PortBindings:  network.PortMap{port: []network.PortBinding{{HostIP: netip.MustParseAddr(bindAddress), HostPort: "8317"}}},
			Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: "vastora-cpa-auths", Target: "/root/.cli-proxy-api"},
				{Type: mount.TypeVolume, Source: "vastora-cpa-logs", Target: "/CLIProxyAPI/logs"},
				{Type: mount.TypeVolume, Source: "vastora-cpa-plugins", Target: "/CLIProxyAPI/plugins"},
			},
		},
		Name: cpaContainer,
	})
	if err != nil {
		return fmt.Errorf("agent: create CPA container: %w", err)
	}
	if err := copyCPAConfig(ctx, docker, created.ID, settings, secrets); err != nil {
		_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
		return err
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("agent: start CPA container: %w", err)
	}
	inspected, err := docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("agent: inspect CPA container: %w", err)
	}
	if inspected.Container.State == nil || !inspected.Container.State.Running {
		return errors.New("agent: CPA container did not remain running")
	}
	return nil
}

func decodeCPAConfig(rawConfig, rawSecrets json.RawMessage) (cpaConfig, cpaSecrets, error) {
	var config cpaConfig
	var secrets cpaSecrets
	if json.Unmarshal(rawConfig, &config) != nil || json.Unmarshal(rawSecrets, &secrets) != nil {
		return config, secrets, errors.New("agent: invalid CPA configuration")
	}
	if config.Timezone == "" || secrets.ManagementKey == "" || secrets.APIKey == "" {
		return config, secrets, errors.New("agent: incomplete CPA configuration")
	}
	return config, secrets, nil
}

func copyCPAConfig(ctx context.Context, docker *client.Client, containerID string, settings cpaConfig, secrets cpaSecrets) error {
	payload, err := json.Marshal(map[string]any{
		"host": "0.0.0.0", "port": 8317, "auth-dir": "/root/.cli-proxy-api", "api-keys": []string{secrets.APIKey}, "debug": settings.Debug,
		"logging-to-file":   true,
		"remote-management": map[string]any{"allow-remote": true, "secret-key": secrets.ManagementKey},
	})
	if err != nil {
		return fmt.Errorf("agent: encode CPA configuration: %w", err)
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "config.yaml", Mode: 0o600, Size: int64(len(payload))}); err != nil {
		return fmt.Errorf("agent: archive CPA configuration: %w", err)
	}
	if _, err := writer.Write(payload); err != nil {
		return fmt.Errorf("agent: archive CPA configuration: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("agent: archive CPA configuration: %w", err)
	}
	if _, err := docker.CopyToContainer(ctx, containerID, client.CopyToContainerOptions{DestinationPath: "/CLIProxyAPI", Content: &archive}); err != nil {
		return fmt.Errorf("agent: write CPA configuration: %w", err)
	}
	return nil
}

func ensureCPANetwork(ctx context.Context, docker *client.Client) error {
	if _, err := docker.NetworkInspect(ctx, cpaNetwork, client.NetworkInspectOptions{}); err == nil {
		return nil
	}
	if _, err := docker.NetworkCreate(ctx, cpaNetwork, client.NetworkCreateOptions{Driver: "bridge"}); err != nil {
		return fmt.Errorf("agent: create CPA network: %w", err)
	}
	return nil
}

func deployKeeper(ctx context.Context, docker *client.Client, task DeploymentTask, bindAddress string) error {
	if task.Manifest.ID != "keeper" || task.Manifest.Version != "1.14.1" {
		return errors.New("agent: unsupported Keeper package")
	}
	imageRef, err := declaredImage(task.Manifest, "keeper")
	if err != nil {
		return err
	}
	var config struct {
		Timezone string `json:"timezone"`
	}
	var secrets struct {
		LoginPassword    string `json:"login_password"`
		CPAManagementKey string `json:"cpa_management_key"`
	}
	if json.Unmarshal(task.Config, &config) != nil || json.Unmarshal(task.Secrets, &secrets) != nil || config.Timezone == "" || secrets.LoginPassword == "" || secrets.CPAManagementKey == "" {
		return errors.New("agent: incomplete Keeper configuration")
	}
	if err := ensureCPANetwork(ctx, docker); err != nil {
		return err
	}
	pull, err := docker.ImagePull(ctx, imageRef, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("agent: pull Keeper image: %w", err)
	}
	_, _ = io.Copy(io.Discard, pull)
	_ = pull.Close()
	_, _ = docker.ContainerRemove(ctx, keeperContainer, client.ContainerRemoveOptions{Force: true})
	keeperPort := network.MustParsePort("8080/tcp")
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: imageRef, Env: []string{"TZ=" + config.Timezone, "CPA_BASE_URL=http://" + cpaContainer + ":8317", "CPA_MANAGEMENT_KEY=" + secrets.CPAManagementKey, "LOGIN_PASSWORD=" + secrets.LoginPassword, "AUTH_ENABLED=true"}, ExposedPorts: network.PortSet{keeperPort: struct{}{}}},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(cpaNetwork), RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")}, PortBindings: network.PortMap{keeperPort: []network.PortBinding{{HostIP: netip.MustParseAddr(bindAddress), HostPort: "8080"}}}, Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: "vastora-cpa-keeper-data", Target: "/data"}}},
		Name:       keeperContainer,
	})
	if err != nil {
		return fmt.Errorf("agent: create Keeper container: %w", err)
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("agent: start Keeper container: %w", err)
	}
	return nil
}

func deployKomari(ctx context.Context, docker *client.Client, task DeploymentTask) error {
	if task.Manifest.ID != "komari-agent" || task.Manifest.Version != "1.2.60" {
		return errors.New("agent: unsupported Komari Agent package")
	}
	imageRef, err := declaredImage(task.Manifest, "komari-agent")
	if err != nil {
		return err
	}
	var config struct {
		Endpoint string `json:"endpoint"`
	}
	var secrets struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(task.Config, &config) != nil || json.Unmarshal(task.Secrets, &secrets) != nil || config.Endpoint == "" || secrets.Token == "" {
		return errors.New("agent: incomplete Komari Agent configuration")
	}
	pull, err := docker.ImagePull(ctx, imageRef, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("agent: pull Komari Agent image: %w", err)
	}
	_, _ = io.Copy(io.Discard, pull)
	_ = pull.Close()
	_, _ = docker.ContainerRemove(ctx, komariContainer, client.ContainerRemoveOptions{Force: true})
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: imageRef, Cmd: []string{"--endpoint", config.Endpoint, "--token", secrets.Token, "--disable-web-ssh"}},
		HostConfig: &container.HostConfig{NetworkMode: "host", PidMode: "host", RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")}, Binds: []string{"/proc:/host/proc:ro", "/sys:/host/sys:ro", "/:/host:ro"}},
		Name:       komariContainer,
	})
	if err != nil {
		return fmt.Errorf("agent: create Komari Agent container: %w", err)
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("agent: start Komari Agent container: %w", err)
	}
	inspected, err := docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("agent: inspect Komari Agent container: %w", err)
	}
	if inspected.Container.State == nil || !inspected.Container.State.Running {
		return errors.New("agent: Komari Agent container did not remain running")
	}
	return nil
}

func reportedServices(ctx context.Context, task DeploymentTask, bindAddress string) (ApplicationTaskResult, error) {
	result := ApplicationTaskResult{Services: make([]ApplicationServiceResult, 0, len(task.Manifest.Services))}
	for _, service := range task.Manifest.Services {
		hostPort, err := serviceHostPort(task.Config, service)
		if err != nil {
			return ApplicationTaskResult{}, err
		}
		if err := waitForEndpoint(ctx, bindAddress, hostPort); err != nil {
			return ApplicationTaskResult{}, fmt.Errorf("agent: service %s did not become ready: %w", service.Name, err)
		}
		result.Services = append(result.Services, ApplicationServiceResult{
			Name: service.Name, Protocol: service.Protocol, ContainerPort: service.ContainerPort,
			HostPort: hostPort, Address: bindAddress,
		})
	}
	return result, nil
}

func serviceHostPort(raw json.RawMessage, service catalog.Service) (int, error) {
	if service.HostPortField == "" {
		return service.DefaultHostPort, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return 0, errors.New("agent: invalid application configuration")
	}
	var port int
	if err := json.Unmarshal(values[service.HostPortField], &port); err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("agent: invalid service port field %q", service.HostPortField)
	}
	return port, nil
}

func waitForEndpoint(ctx context.Context, address string, port int) error {
	readyContext, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	target := net.JoinHostPort(address, strconv.Itoa(port))
	dialer := net.Dialer{Timeout: time.Second}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		connection, err := dialer.DialContext(readyContext, "tcp", target)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		select {
		case <-readyContext.Done():
			return readyContext.Err()
		case <-ticker.C:
		}
	}
}

func declaredImage(manifest catalog.AppManifest, name string) (string, error) {
	for _, image := range manifest.Images {
		if image.Name == name {
			return image.Reference, nil
		}
	}
	return "", fmt.Errorf("agent: image %q is missing from the signed manifest", name)
}
