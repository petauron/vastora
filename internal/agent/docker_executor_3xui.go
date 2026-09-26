package agent

import (
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
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/networking"
)

const (
	threeXUIRealityPort = 443
)

func threeXUIPorts(bindAddress string, panelPort int, role string) (dockernetwork.PortSet, dockernetwork.PortMap, error) {
	if role != "master" {
		return nil, nil, errors.New("agent: only the controller adapter may deploy 3x-ui")
	}
	if err := validateThreeXUIServiceAddress(bindAddress, panelPort, role); err != nil {
		return nil, nil, err
	}
	address := netip.MustParseAddr(bindAddress)
	exposed := dockernetwork.PortSet{}
	bindings := dockernetwork.PortMap{}
	expose := func(portNumber int) dockernetwork.Port {
		port := dockernetwork.MustParsePort(strconv.Itoa(portNumber) + "/tcp")
		exposed[port] = struct{}{}
		return port
	}
	bind := func(portNumber int) {
		port := expose(portNumber)
		bindings[port] = []dockernetwork.PortBinding{{HostIP: address.Unmap(), HostPort: strconv.Itoa(portNumber)}}
	}
	bind(panelPort)
	// REALITY is reachable only from the per-node HAProxy over the shared
	// Docker network. Never publish the raw controller adapter socket.
	expose(threeXUIRealityPort)
	return exposed, bindings, nil
}

func validateThreeXUIServiceAddress(bindAddress string, panelPort int, role string) error {
	address, err := netip.ParseAddr(bindAddress)
	if err != nil || !address.Unmap().Is4() {
		return errors.New("agent: proxy runtime requires a valid IPv4 service address")
	}
	if !networking.IsPrivateServiceAddress(address.String()) {
		return errors.New("agent: proxy runtime requires a LAN, Headscale/Tailscale, or loopback service address")
	}
	if role != "master" && role != "worker" {
		return errors.New("agent: invalid proxy topology role")
	}
	if panelPort == threeXUIRealityPort {
		return errors.New("agent: proxy management port must not use the managed REALITY port")
	}
	return nil
}

func threeXUIRecoveryErrorIsPostCommitCleanup(ctx context.Context, docker threeXUIContainerEngine, deploymentID string) (bool, error) {
	current, currentExists, err := inspectThreeXUIContainer(ctx, docker, threeXUIContainer)
	if err != nil || !currentExists {
		return false, err
	}
	if current.Container.Config == nil || current.Container.Config.Labels[threeXUIDeploymentIDLabel] != deploymentID {
		return false, nil
	}
	_, candidateExists, err := inspectThreeXUIContainer(ctx, docker, threeXUICandidateContainer)
	if err != nil {
		return false, err
	}
	_, rollbackExists, err := inspectThreeXUIContainer(ctx, docker, threeXUIBackupContainer)
	if err != nil {
		return false, err
	}
	// A rollback or candidate marker proves promotion is not committed. A
	// cleanup marker is the explicit commit record and may safely be left for
	// maintenance if only its removal failed.
	return !candidateExists && !rollbackExists, nil
}

func committedThreeXUIDeploymentToken(ctx context.Context, docker *client.Client, deploymentID, bindAddress string, panelPort int) (string, bool, error) {
	if strings.TrimSpace(deploymentID) == "" {
		return "", false, nil
	}
	current, exists, err := inspectThreeXUIContainer(ctx, docker, threeXUIContainer)
	if err != nil || !exists {
		return "", false, err
	}
	if current.Container.Config == nil || current.Container.Config.Labels[threeXUIDeploymentIDLabel] != deploymentID {
		return "", false, nil
	}
	if err := waitForBindAddress(ctx, bindAddress); err != nil {
		return "", true, err
	}
	if current.Container.State == nil || !current.Container.State.Running {
		if err := startCommittedThreeXUIDeployment(ctx, docker, current); err != nil {
			return "", true, err
		}
	}
	if err := dockerruntime.RecoverAttachment(ctx, docker, current.Container.ID, dockerruntime.NetworkName, "runtime-network", dockerruntime.ThreeXUIAlias); err != nil {
		return "", true, err
	}
	if err := waitForEndpoint(ctx, bindAddress, panelPort); err != nil {
		return "", true, fmt.Errorf("agent: committed 3x-ui deployment is not healthy: %w", err)
	}
	token, err := threeXUIAPIToken(ctx, docker, current.Container.ID)
	if err != nil {
		return "", true, err
	}
	return token, true, nil
}

func startCommittedThreeXUIDeployment(ctx context.Context, docker threeXUIContainerEngine, current client.ContainerInspectResult) error {
	if current.Container.State != nil && current.Container.State.Running {
		return nil
	}
	_, startErr := docker.ContainerStart(ctx, current.Container.ID, client.ContainerStartOptions{})
	if startErr == nil || errdefs.IsNotModified(startErr) {
		return nil
	}
	// A same-ID task replay is explicit evidence that this stopped container is
	// the deployment being reconciled. If Docker lost the Start response, prove
	// the committed result by immutable ID instead of quarantining forever.
	inspected, inspectErr := docker.ContainerInspect(ctx, current.Container.ID, client.ContainerInspectOptions{})
	if inspectErr == nil && inspected.Container.State != nil && inspected.Container.State.Running {
		return nil
	}
	return errors.Join(fmt.Errorf("agent: restart committed 3x-ui deployment: %w", startErr), inspectErr)
}

func configureThreeXUISubscriptionRole(ctx context.Context, address string, panelPort int, apiToken, role string) error {
	if role != "master" && role != "worker" {
		return errors.New("agent: invalid 3x-ui topology role")
	}
	baseURL := "http://" + net.JoinHostPort(address, strconv.Itoa(panelPort))
	settings, err := threeXUIRequest(ctx, http.MethodPost, baseURL+"/panel/api/setting/all", apiToken, map[string]any{})
	if err != nil {
		return fmt.Errorf("agent: read 3x-ui subscription settings: %w", err)
	}
	settings["subEnable"] = false
	settings["subClashEnable"] = false
	if _, err := threeXUIRequest(ctx, http.MethodPost, baseURL+"/panel/api/setting/update", apiToken, settings); err != nil {
		return fmt.Errorf("agent: disable superseded 3x-ui subscription service: %w", err)
	}
	return restartThreeXUIPanel(ctx, baseURL, apiToken, threeXUIRestartSettleTime)
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
	command := []string{"/app/x-ui", "setting", "-webBasePath", "/", "-listenIP", "0.0.0.0", "-port", strconv.Itoa(panelPort), "-username", credentials.Username, "-password", credentials.Password}
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
