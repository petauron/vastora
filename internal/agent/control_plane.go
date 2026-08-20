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
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/networking"
)

var Version = "0.1.0-dev"

type Client struct {
	HTTPClient         *http.Client
	Executor           Executor
	Roles              []string
	Capabilities       Capabilities
	GatewayDriver      GatewayDriver
	GatewayProvisioner GatewayProvisioner
	TunnelProvisioner  TunnelProvisioner
}

type Capabilities struct {
	Docker  bool `json:"docker"`
	Gateway bool `json:"gateway"`
	Tunnel  bool `json:"tunnel"`
	Metrics bool `json:"metrics"`
	Logs    bool `json:"logs"`
}

type Enrollment struct {
	ID           string       `json:"id"`
	Credential   string       `json:"credential"`
	Name         string       `json:"name"`
	Roles        []string     `json:"roles"`
	Capabilities Capabilities `json:"capabilities"`
}

type DeploymentTask struct {
	Kind           string                `json:"kind"`
	ID             string                `json:"id"`
	Attempt        int64                 `json:"attempt"`
	AppKey         string                `json:"appKey"`
	Manifest       catalog.AppManifest   `json:"manifest"`
	Config         json.RawMessage       `json:"config"`
	Secrets        json.RawMessage       `json:"secrets"`
	Operation      string                `json:"operation"`
	DeleteData     bool                  `json:"deleteData"`
	Revision       int64                 `json:"revision,omitempty"`
	ApplicationID  string                `json:"applicationId,omitempty"`
	ServiceAddress string                `json:"serviceAddress,omitempty"`
	GatewayState   *gateway.DesiredState `json:"gatewayState,omitempty"`
	TunnelState    *TunnelDesiredState   `json:"tunnelState,omitempty"`
}

type Executor interface {
	Deploy(context.Context, DeploymentTask) (ApplicationTaskResult, error)
}

type ApplicationServiceResult struct {
	Name          string `json:"name"`
	Protocol      string `json:"protocol"`
	ContainerPort int    `json:"containerPort"`
	HostPort      int    `json:"hostPort"`
	Address       string `json:"address"`
}

type ApplicationTaskResult struct {
	Services         []ApplicationServiceResult `json:"services"`
	GeneratedSecrets map[string]string          `json:"generatedSecrets,omitempty"`
}

type ApplicationEndpointObservation struct {
	AppKey      string `json:"appKey"`
	Name        string `json:"name"`
	Protocol    string `json:"protocol"`
	AppProtocol string `json:"appProtocol"`
	Listen      string `json:"listen"`
	Port        int    `json:"port"`
	Enabled     bool   `json:"enabled"`
}

func (c Client) Enroll(ctx context.Context, store *Store, centerURL, enrollmentToken string) (Enrollment, error) {
	baseURL, err := normalizeCenterURL(centerURL)
	if err != nil {
		return Enrollment{}, err
	}
	if strings.TrimSpace(enrollmentToken) == "" {
		return Enrollment{}, errors.New("agent: enrollment token is required")
	}
	var response Enrollment
	if err := c.post(ctx, baseURL+"/api/v1/agents/enroll", map[string]string{
		"token": enrollmentToken, "version": Version,
	}, "", &response); err != nil {
		return Enrollment{}, err
	}
	if response.ID == "" || response.Credential == "" || strings.TrimSpace(response.Name) == "" || len(response.Roles) == 0 {
		return Enrollment{}, errors.New("agent: Center returned an incomplete enrollment response")
	}
	if err := store.SaveConnection(ctx, Connection{AgentID: response.ID, Name: response.Name, CenterURL: baseURL, Credential: response.Credential}); err != nil {
		return Enrollment{}, err
	}
	return response, nil
}

func (c Client) Heartbeat(ctx context.Context, store *Store) error {
	_, err := c.heartbeat(ctx, store)
	return err
}

func (c Client) heartbeat(ctx context.Context, store *Store) (error, error) {
	connection, err := store.Connection(ctx)
	if err != nil {
		return nil, err
	}
	states, err := store.ListApplied(ctx)
	if err != nil {
		return nil, err
	}
	gatewayHealthy := false
	if c.GatewayDriver != nil {
		gatewayHealthy = c.GatewayDriver.Health(ctx) == nil
	}
	candidates, err := networking.Discover(time.Now())
	if err != nil {
		return nil, fmt.Errorf("agent: discover network addresses: %w", err)
	}
	endpoints, observeErr := observeThreeXUI(ctx, store)
	endpointsObserved := observeErr == nil || errors.Is(observeErr, errApplicationNotInstalled)
	if errors.Is(observeErr, errApplicationNotInstalled) {
		observeErr = nil
	} else if observeErr != nil {
		observeErr = fmt.Errorf("agent: observe 3x-ui: %w", observeErr)
	}
	err = c.post(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/heartbeat", map[string]any{
		"version": Version, "appliedInstallations": len(states), "roles": c.Roles,
		"capabilities": c.Capabilities, "networkCandidates": candidates, "applicationEndpoints": endpoints, "applicationEndpointsObserved": endpointsObserved, "gatewayHealthy": gatewayHealthy,
	}, connection.Credential, nil)
	return observeErr, err
}

func observeThreeXUI(ctx context.Context, store *Store) ([]ApplicationEndpointObservation, error) {
	installation, err := store.AppliedInstallation(ctx, threeXUIKey)
	if err != nil {
		return nil, err
	}
	config, err := decodeThreeXUIConfig(installation.Config)
	if err != nil {
		return nil, err
	}
	var secretValues map[string]string
	if json.Unmarshal(installation.Secrets, &secretValues) != nil || strings.TrimSpace(secretValues["api_token"]) == "" {
		return nil, errors.New("agent: 3x-ui API token is unavailable")
	}
	address := installation.ServiceAddress
	if net.ParseIP(address) == nil {
		address = "127.0.0.1"
	}
	endpoint := "http://" + net.JoinHostPort(address, strconv.Itoa(config.PanelPort)) + "/panel/api/inbounds/list"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+secretValues["api_token"])
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var payload struct {
		Success bool `json:"success"`
		Object  []struct {
			ID       int             `json:"id"`
			Remark   string          `json:"remark"`
			Protocol string          `json:"protocol"`
			Port     int             `json:"port"`
			Listen   string          `json:"listen"`
			Enable   bool            `json:"enable"`
			Stream   json.RawMessage `json:"streamSettings"`
		} `json:"obj"`
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&payload) != nil || !payload.Success {
		return nil, errors.New("agent: 3x-ui inbound API request failed")
	}
	result := make([]ApplicationEndpointObservation, 0, len(payload.Object))
	for _, inbound := range payload.Object {
		transport := "tcp"
		var stream struct {
			Network string `json:"network"`
		}
		if json.Unmarshal(inbound.Stream, &stream) == nil && stream.Network != "" {
			transport = strings.ToLower(stream.Network)
		}
		name := "inbound-" + strconv.Itoa(inbound.ID)
		result = append(result, ApplicationEndpointObservation{AppKey: threeXUIKey, Name: name, Protocol: "tcp", AppProtocol: strings.ToLower(inbound.Protocol) + "/" + transport, Listen: strings.TrimSpace(inbound.Listen), Port: inbound.Port, Enabled: inbound.Enable})
	}
	return result, nil
}

func (c Client) RunHeartbeats(ctx context.Context, store *Store, interval time.Duration, report func(error)) {
	if interval < time.Second {
		interval = 15 * time.Second
	}
	restoreContext, restoreCancel := context.WithTimeout(ctx, 5*time.Minute)
	if gatewayErr := restoreGatewayState(restoreContext, store, c.GatewayDriver); gatewayErr != nil && report != nil {
		report(gatewayErr)
	}
	restoreCancel()
	send := func() {
		requestContext, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		observeErr, err := c.heartbeat(requestContext, store)
		if observeErr != nil && report != nil {
			report(observeErr)
		}
		if err != nil {
			if report != nil {
				report(err)
			}
			return
		}
		task, err := c.claimNextTask(requestContext, store)
		if err != nil {
			if report != nil {
				report(err)
			}
			return
		}
		if task == nil {
			return
		}
		var result ApplicationTaskResult
		switch task.Kind {
		case "application.apply":
			if c.Executor == nil || !c.Capabilities.Docker {
				err = errors.New("agent: Docker capability is not configured")
			} else {
				result, err = c.Executor.Deploy(requestContext, *task)
			}
		case "gateway.routes.apply":
			if task.GatewayState == nil || !c.Capabilities.Gateway {
				err = errors.New("agent: gateway task received without gateway capability")
			} else {
				err = applyGatewayDesiredState(requestContext, store, c.GatewayDriver, *task.GatewayState)
			}
		case "gateway.component.apply":
			if c.GatewayProvisioner == nil || !c.Capabilities.Gateway {
				err = errors.New("agent: gateway provisioning capability is not configured")
			} else if task.Operation == "running" {
				err = c.GatewayProvisioner.Ensure(requestContext)
				if err == nil {
					err = waitForGateway(requestContext, c.GatewayDriver)
				}
			} else if task.Operation == "stopped" {
				err = c.GatewayProvisioner.Remove(requestContext)
				if err == nil {
					err = store.ClearGatewayState(requestContext)
				}
			} else {
				err = errors.New("agent: invalid gateway component operation")
			}
		case "tunnel.state.apply":
			if c.TunnelProvisioner == nil || !c.Capabilities.Tunnel || task.TunnelState == nil {
				err = errors.New("agent: tunnel task received without tunnel capability")
			} else {
				err = c.TunnelProvisioner.Apply(requestContext, *task.TunnelState)
			}
		default:
			err = errors.New("agent: unsupported task kind")
		}
		if err == nil && task.Kind == "application.apply" && task.Operation != "uninstall" {
			if len(result.GeneratedSecrets) != 0 {
				task.Secrets, err = mergeGeneratedSecrets(task.Secrets, result.GeneratedSecrets)
			}
		}
		if err == nil && task.Kind == "application.apply" && task.Operation != "uninstall" {
			_, err = store.RecordApplied(requestContext, AppliedInstallation{InstanceID: task.ID, AppKey: task.AppKey, Version: task.Manifest.Version, Config: task.Config, Secrets: task.Secrets, ServiceAddress: task.ServiceAddress})
		}
		if err == nil && task.Kind == "application.apply" && task.Operation == "uninstall" {
			err = store.RemoveApplied(requestContext, task.AppKey)
		}
		if completeErr := c.completeTask(requestContext, store, task.ID, task.Attempt, result, err); completeErr != nil && report != nil {
			report(completeErr)
		}
		if err != nil && report != nil {
			report(err)
		}
	}
	send()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

func mergeGeneratedSecrets(raw json.RawMessage, generated map[string]string) (json.RawMessage, error) {
	values := map[string]any{}
	if len(raw) != 0 && json.Unmarshal(raw, &values) != nil {
		return nil, errors.New("agent: stored task secrets are invalid")
	}
	for key, value := range generated {
		values[key] = value
	}
	return json.Marshal(values)
}

func waitForGateway(ctx context.Context, driver GatewayDriver) error {
	if driver == nil {
		return errors.New("agent: gateway driver is not configured")
	}
	readyContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := driver.Health(readyContext); err == nil {
			return nil
		}
		select {
		case <-readyContext.Done():
			return errors.New("agent: Caddy gateway did not become healthy")
		case <-ticker.C:
		}
	}
}

func (c Client) claimNextTask(ctx context.Context, store *Store) (*DeploymentTask, error) {
	connection, err := store.Connection(ctx)
	if err != nil {
		return nil, err
	}
	var response struct {
		Task *DeploymentTask `json:"task"`
	}
	if err := c.get(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/tasks/next", connection.Credential, &response); err != nil {
		return nil, err
	}
	return response.Task, nil
}

func (c Client) completeTask(ctx context.Context, store *Store, taskID string, attempt int64, result ApplicationTaskResult, deploymentErr error) error {
	connection, err := store.Connection(ctx)
	if err != nil {
		return err
	}
	payload := map[string]any{"attempt": attempt, "succeeded": deploymentErr == nil, "error": "", "result": result}
	if deploymentErr != nil {
		payload["error"] = deploymentErr.Error()
	}
	return c.post(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/tasks/"+url.PathEscape(taskID)+"/result", payload, connection.Credential, nil)
}

func (c Client) post(ctx context.Context, endpoint string, payload any, credential string, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("agent: encode Center request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("agent: create Center request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("agent: request Center: %w", err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("agent: read Center response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(content, &failure)
		if failure.Error == "" {
			failure.Error = response.Status
		}
		return fmt.Errorf("agent: Center request failed: %s", failure.Error)
	}
	if target != nil && json.Unmarshal(content, target) != nil {
		return errors.New("agent: Center returned invalid JSON")
	}
	return nil
}

func (c Client) get(ctx context.Context, endpoint, credential string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("agent: create Center request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("agent: request Center: %w", err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("agent: read Center response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("agent: Center request failed: %s", response.Status)
	}
	if err := json.Unmarshal(content, target); err != nil {
		return errors.New("agent: Center returned invalid JSON")
	}
	return nil
}

func normalizeCenterURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("agent: Center URL must be an absolute URL without credentials, query, or fragment")
	}
	if parsed.Scheme == "https" {
		return strings.TrimRight(parsed.String(), "/"), nil
	}
	if parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname()) {
		return "", errors.New("agent: Center URL must use HTTPS unless it is loopback HTTP")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
