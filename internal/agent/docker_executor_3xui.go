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
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/networking"
)

const (
	threeXUISubscriptionPort = 2096
	threeXUIRealityPortFirst = 20000
	threeXUIRealityPortLast  = 20031
)

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
	// Recover a previously interrupted replacement before any registry access.
	// An image pull failure must never leave the last known-good instance down.
	if err := recoverInterruptedThreeXUIDeploy(ctx, docker); err != nil {
		// Cleanup of a stale candidate/rollback marker can fail after this exact
		// deployment was already promoted. Do not false-fail the committed task:
		// the immutable deployment label lets the token fast path reconcile it,
		// while maintenance keeps retrying non-critical marker cleanup.
		committed, inspectErr := threeXUIRecoveryErrorIsPostCommitCleanup(ctx, docker, task.ID)
		if inspectErr != nil || !committed {
			cause := errors.Join(err, inspectErr)
			if task.Attempt > 1 {
				return "", deferTaskUntilReconciled(cause)
			}
			return "", cause
		}
	}
	if token, committed, err := committedThreeXUIDeploymentToken(ctx, docker, task.ID, bindAddress, settings.PanelPort); err != nil {
		return "", deferTaskUntilReconciled(err)
	} else if committed {
		baseURL := "http://" + net.JoinHostPort(bindAddress, strconv.Itoa(settings.PanelPort))
		if _, err := threeXUIRequest(ctx, http.MethodPost, baseURL+"/panel/api/setting/all", token, map[string]any{}); err != nil {
			return "", deferTaskUntilReconciled(fmt.Errorf("agent: verify reconciled 3x-ui API: %w", err))
		}
		if _, err := reportedServices(ctx, task, bindAddress); err != nil {
			return "", deferTaskUntilReconciled(err)
		}
		return token, nil
	}
	pull, err := docker.ImagePull(ctx, imageRef, client.ImagePullOptions{})
	if err != nil {
		return "", fmt.Errorf("agent: pull 3x-ui image: %w", err)
	}
	_, _ = io.Copy(io.Discard, pull)
	_ = pull.Close()
	exposedPorts, portBindings, err := threeXUIPorts(bindAddress, settings.PanelPort, task.ApplicationRole)
	if err != nil {
		return "", err
	}
	createOptions := client.ContainerCreateOptions{
		Config: &container.Config{
			Image:        imageRef,
			Tty:          true,
			ExposedPorts: exposedPorts,
			Labels: map[string]string{
				threeXUIDeploymentIDLabel: task.ID,
			},
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
			NetworkMode:   container.NetworkMode(dockerruntime.NetworkName),
			PortBindings:  portBindings,
			Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: threeXUIDatabaseVolume, Target: "/etc/x-ui"},
				{Type: mount.TypeVolume, Source: "vastora-3x-ui-cert", Target: "/root/cert"},
				{Type: mount.TypeVolume, Source: "vastora-3x-ui-acme", Target: "/root/.acme.sh"},
			},
		},
		NetworkingConfig: dockerruntime.NetworkingConfig(dockerruntime.ThreeXUIAlias),
		Name:             threeXUICandidateContainer,
	}
	return replaceThreeXUIContainer(ctx, docker, createOptions, func(containerID string) (string, error) {
		if err := configureThreeXUI(ctx, docker, containerID, bindAddress, settings.PanelPort, credentials); err != nil {
			return "", err
		}
		apiToken, err := threeXUIAPIToken(ctx, docker, containerID)
		if err != nil {
			return "", err
		}
		if err := configureThreeXUISubscriptionRole(ctx, bindAddress, settings.PanelPort, apiToken, task.ApplicationRole); err != nil {
			return "", err
		}
		return apiToken, nil
	}, func(containerID, apiToken string) error {
		inspected, err := docker.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
		if err != nil {
			return fmt.Errorf("inspect promoted container: %w", err)
		}
		if inspected.Container.State == nil || !inspected.Container.State.Running {
			return errors.New("promoted container is not running")
		}
		baseURL := "http://" + net.JoinHostPort(bindAddress, strconv.Itoa(settings.PanelPort))
		if _, err := threeXUIRequest(ctx, http.MethodPost, baseURL+"/panel/api/setting/all", apiToken, map[string]any{}); err != nil {
			return fmt.Errorf("verify promoted 3x-ui API: %w", err)
		}
		if _, err := reportedServices(ctx, task, bindAddress); err != nil {
			return err
		}
		return nil
	})
}

func threeXUIPorts(bindAddress string, panelPort int, role string) (dockernetwork.PortSet, dockernetwork.PortMap, error) {
	address, err := netip.ParseAddr(bindAddress)
	if err != nil || !address.Unmap().Is4() {
		return nil, nil, errors.New("agent: 3x-ui requires a valid IPv4 service address")
	}
	if !networking.IsPrivateServiceAddress(address.String()) {
		return nil, nil, errors.New("agent: 3x-ui REALITY ports require a LAN, Headscale/Tailscale, or loopback service address")
	}
	if role != "master" && role != "worker" {
		return nil, nil, errors.New("agent: invalid 3x-ui topology role")
	}
	exposed := dockernetwork.PortSet{}
	bindings := dockernetwork.PortMap{}
	add := func(portNumber int) {
		port := dockernetwork.MustParsePort(strconv.Itoa(portNumber) + "/tcp")
		exposed[port] = struct{}{}
		bindings[port] = []dockernetwork.PortBinding{{HostIP: address.Unmap(), HostPort: strconv.Itoa(portNumber)}}
	}
	add(panelPort)
	if role == "master" {
		add(threeXUISubscriptionPort)
	}
	for port := threeXUIRealityPortFirst; port <= threeXUIRealityPortLast; port++ {
		add(port)
	}
	return exposed, bindings, nil
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
	if current.Container.State == nil || !current.Container.State.Running {
		if err := startCommittedThreeXUIDeployment(ctx, docker, current); err != nil {
			return "", true, err
		}
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
	if role == "master" {
		return configureThreeXUISubscription(ctx, address, panelPort, apiToken)
	}
	if role != "worker" {
		return errors.New("agent: invalid 3x-ui topology role")
	}
	baseURL := "http://" + net.JoinHostPort(address, strconv.Itoa(panelPort))
	settings, err := threeXUIRequest(ctx, http.MethodPost, baseURL+"/panel/api/setting/all", apiToken, map[string]any{})
	if err != nil {
		return fmt.Errorf("agent: read 3x-ui worker settings: %w", err)
	}
	settings["subEnable"] = false
	settings["subClashEnable"] = false
	if _, err := threeXUIRequest(ctx, http.MethodPost, baseURL+"/panel/api/setting/update", apiToken, settings); err != nil {
		return fmt.Errorf("agent: disable standalone 3x-ui worker subscription: %w", err)
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

func configureThreeXUISubscription(ctx context.Context, address string, panelPort int, apiToken string) error {
	baseURL := "http://" + net.JoinHostPort(address, strconv.Itoa(panelPort))
	settings, err := threeXUIRequest(ctx, http.MethodPost, baseURL+"/panel/api/setting/all", apiToken, map[string]any{})
	if err != nil {
		return fmt.Errorf("agent: read 3x-ui subscription settings: %w", err)
	}
	for key, value := range threeXUIManagedSubscriptionSettings() {
		settings[key] = value
	}
	settings["subListen"] = "0.0.0.0"
	settings["subPort"] = threeXUISubscriptionPort
	if _, err := threeXUIRequest(ctx, http.MethodPost, baseURL+"/panel/api/setting/update", apiToken, settings); err != nil {
		return fmt.Errorf("agent: update 3x-ui subscription settings: %w", err)
	}
	if err := restartThreeXUIPanel(ctx, baseURL, apiToken, threeXUIRestartSettleTime); err != nil {
		return err
	}
	return waitForEndpoint(ctx, address, threeXUISubscriptionPort)
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
