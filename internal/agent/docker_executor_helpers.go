package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/distribution/reference"
	"github.com/moby/moby/api/pkg/authconfig"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/catalog"
)

func reportedServices(ctx context.Context, task DeploymentTask, bindAddress string) (ApplicationTaskResult, error) {
	result := ApplicationTaskResult{Services: make([]ApplicationServiceResult, 0, len(task.Manifest.Services))}
	for _, service := range task.Manifest.Services {
		// Center verifies the published subscription independently. During
		// startup recovery, a disabled or unhealthy subscription must not block
		// the Agent from receiving the role-reconciliation task that repairs it.
		if task.AppKey == threeXUIKey && service.Name == "subscription" && (task.ApplicationRole == "worker" || task.OfflineRestore) {
			continue
		}
		hostPort, err := serviceHostPort(task.Config, service)
		if err != nil {
			return ApplicationTaskResult{}, err
		}
		if err := waitForServiceEndpoint(ctx, bindAddress, hostPort, service); err != nil {
			return ApplicationTaskResult{}, fmt.Errorf("agent: service %s did not become ready: %w", service.Name, err)
		}
		result.Services = append(result.Services, ApplicationServiceResult{
			Name: service.Name, Protocol: service.Protocol, ContainerPort: service.ContainerPort,
			HostPort: hostPort, Address: bindAddress,
		})
	}
	return result, nil
}

func waitForServiceEndpoint(ctx context.Context, address string, port int, service catalog.Service) error {
	if err := waitForEndpoint(ctx, address, port); err != nil {
		return err
	}
	if service.HealthPath == "" {
		return nil
	}
	readyContext, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	target := service.Protocol + "://" + net.JoinHostPort(address, strconv.Itoa(port)) + service.HealthPath
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		request, err := http.NewRequestWithContext(readyContext, http.MethodGet, target, nil)
		if err != nil {
			return fmt.Errorf("invalid health endpoint: %w", err)
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode < http.StatusInternalServerError {
				return nil
			}
			lastErr = fmt.Errorf("health endpoint returned %s", response.Status)
		} else {
			lastErr = err
		}
		select {
		case <-readyContext.Done():
			if lastErr != nil {
				return lastErr
			}
			return readyContext.Err()
		case <-ticker.C:
		}
	}
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

func pullDeclaredImage(ctx context.Context, docker *client.Client, task DeploymentTask, name string) (string, error) {
	imageReference, err := declaredImage(task.Manifest, name)
	if err != nil {
		return "", err
	}
	if task.OfflineRestore {
		if _, err := docker.ImageInspect(ctx, imageReference); err != nil {
			return "", fmt.Errorf("agent: offline restore requires cached image %s: %w", imageReference, err)
		}
		return imageReference, nil
	}
	options, err := declaredImagePullOptions(task, imageReference)
	if err != nil {
		return "", err
	}
	pull, err := docker.ImagePull(ctx, imageReference, options)
	if err != nil {
		return "", redactRegistryPullError(err, task)
	}
	defer pull.Close()
	if err := pull.Wait(ctx); err != nil {
		return "", redactRegistryPullError(err, task)
	}
	return imageReference, nil
}

func redactRegistryPullError(err error, task DeploymentTask) error {
	message := err.Error()
	if task.RegistryCredential != nil && task.RegistryCredential.Password != "" {
		message = strings.ReplaceAll(message, task.RegistryCredential.Password, "[redacted]")
	}
	return errors.New("agent: pull declared image: " + message)
}

func declaredImagePullOptions(task DeploymentTask, imageReference string) (client.ImagePullOptions, error) {
	options := client.ImagePullOptions{}
	if task.RegistryCredential != nil {
		named, parseErr := reference.ParseNormalizedNamed(imageReference)
		if parseErr != nil || !strings.EqualFold(reference.Domain(named), strings.TrimSpace(task.RegistryCredential.Host)) {
			return client.ImagePullOptions{}, errors.New("agent: Registry credential does not match the declared image authority")
		}
		registryAuth, err := authconfig.Encode(registry.AuthConfig{
			ServerAddress: task.RegistryCredential.Host,
			Username:      task.RegistryCredential.Username,
			Password:      task.RegistryCredential.Password,
		})
		if err != nil {
			return client.ImagePullOptions{}, errors.New("agent: encode ephemeral Registry credential")
		}
		options.RegistryAuth = registryAuth
	}
	return options, nil
}
