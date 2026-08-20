package deployer

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/deployapi"
)

const (
	DefaultHeadscaleImage     = "ghcr.io/juanfont/headscale:0.29.3@sha256:0e7f1c6e4ce6c2a2a001103ecd3fa645a045adf30ac8a5234fe037b43000cd72"
	DefaultCaddyImage         = "docker.io/library/caddy:2.11.4@sha256:df7f1c2fb114453b951de51a98efc010db1655a92c2e86be6706714e2417a78d"
	DefaultHeadscaleContainer = "vastora-center-headscale"
	DefaultGatewayContainer   = "vastora-center-gateway"
)

type DockerHeadscaleInstaller struct {
	Socket                string
	ConfigDir             string
	CenterOrigin          string
	CenterDataVolume      string
	HeadscaleDataVolume   string
	HeadscaleConfigVolume string
	CaddyDataVolume       string
	CaddyConfigVolume     string
	HeadscaleImage        string
	CaddyImage            string
	HTTPClient            *http.Client
}

func (installer DockerHeadscaleInstaller) InstallHeadscale(ctx context.Context, input deployapi.HeadscaleInstallRequest) (deployapi.HeadscaleInstallResult, error) {
	endpoint, apiKey, err := installer.applyHeadscale(ctx, input, true)
	if err != nil {
		return deployapi.HeadscaleInstallResult{}, err
	}
	return deployapi.HeadscaleInstallResult{Endpoint: endpoint, APIKey: apiKey}, nil
}

func (installer DockerHeadscaleInstaller) ReconcileHeadscale(ctx context.Context, input deployapi.HeadscaleInstallRequest) error {
	_, _, err := installer.applyHeadscale(ctx, input, false)
	return err
}

func (installer DockerHeadscaleInstaller) applyHeadscale(ctx context.Context, input deployapi.HeadscaleInstallRequest, createAPIKey bool) (string, string, error) {
	settings, centerURL, headscaleURL, err := installer.settings(input)
	if err != nil {
		return "", "", err
	}
	bindAddresses, err := gatewayBindAddresses(ctx, centerURL, headscaleURL)
	if err != nil {
		return "", "", err
	}
	docker, err := client.New(client.WithHost(settings.Socket))
	if err != nil {
		return "", "", fmt.Errorf("deployer: connect Docker: %w", err)
	}
	defer docker.Close()
	if err := writeAtomic(filepath.Join(settings.ConfigDir, "config.yaml"), renderHeadscaleConfig(headscaleURL), 0o644); err != nil {
		return "", "", err
	}
	if err := writeAtomic(filepath.Join(settings.ConfigDir, "policy.hujson"), renderHeadscalePolicy(), 0o644); err != nil {
		return "", "", err
	}
	for _, image := range []string{settings.HeadscaleImage, settings.CaddyImage} {
		pull, err := docker.ImagePull(ctx, image, client.ImagePullOptions{})
		if err != nil {
			return "", "", fmt.Errorf("deployer: pull fixed infrastructure image: %w", err)
		}
		_, _ = io.Copy(io.Discard, pull)
		_ = pull.Close()
	}
	for _, name := range []string{settings.HeadscaleDataVolume, settings.HeadscaleConfigVolume, settings.CaddyDataVolume, settings.CaddyConfigVolume} {
		if _, err := docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name}); err != nil {
			return "", "", fmt.Errorf("deployer: create volume %s: %w", name, err)
		}
	}
	gatewayExists, err := managedContainerExists(ctx, docker, DefaultGatewayContainer, "center-headscale-gateway")
	if err != nil {
		return "", "", err
	}
	if !gatewayExists {
		if err := ensurePortsAvailable(80, 443); err != nil {
			return "", "", err
		}
	}
	if err := settings.replaceHeadscale(ctx, docker); err != nil {
		return "", "", err
	}
	if err := waitForURL(ctx, settings.HTTPClient, "http://127.0.0.1:8081/health", 90*time.Second); err != nil {
		return "", "", fmt.Errorf("deployer: Headscale did not become healthy: %w", err)
	}
	apiKey := ""
	if createAPIKey {
		apiKey, err = createHeadscaleAPIKey(ctx, docker, DefaultHeadscaleContainer)
		if err != nil {
			return "", "", err
		}
	}
	if err := settings.replaceGateway(ctx, docker, renderCaddyfile(centerURL, settings.CenterOrigin, headscaleURL, bindAddresses)); err != nil {
		return "", "", err
	}
	for _, health := range []struct {
		endpoint string
		path     string
	}{{centerURL, "/healthz"}, {headscaleURL, "/health"}} {
		if err := waitForLocalGateway(ctx, health.endpoint, health.path, 3*time.Minute); err != nil {
			return "", "", fmt.Errorf("deployer: HTTPS gateway did not make %s%s healthy: %w", health.endpoint, health.path, err)
		}
	}
	return headscaleURL, apiKey, nil
}

func (installer DockerHeadscaleInstaller) settings(input deployapi.HeadscaleInstallRequest) (DockerHeadscaleInstaller, string, string, error) {
	centerURL, err := normalizePublicURL(input.CenterURL)
	if err != nil {
		return DockerHeadscaleInstaller{}, "", "", fmt.Errorf("deployer: Center URL: %w", err)
	}
	headscaleURL, err := normalizePublicURL(input.HeadscaleURL)
	if err != nil {
		return DockerHeadscaleInstaller{}, "", "", fmt.Errorf("deployer: Headscale URL: %w", err)
	}
	if centerURL == headscaleURL {
		return DockerHeadscaleInstaller{}, "", "", errors.New("deployer: Center and Headscale require different hostnames")
	}
	if installer.Socket == "" {
		installer.Socket = "unix:///var/run/docker.sock"
	}
	if installer.ConfigDir == "" || !filepath.IsAbs(installer.ConfigDir) {
		return DockerHeadscaleInstaller{}, "", "", errors.New("deployer: absolute Headscale config directory is required")
	}
	if installer.CenterOrigin == "" {
		installer.CenterOrigin = "127.0.0.1:8080"
	}
	if installer.CenterDataVolume == "" {
		installer.CenterDataVolume = "vastora_center-data"
	}
	if installer.HeadscaleDataVolume == "" {
		installer.HeadscaleDataVolume = "vastora_headscale-data"
	}
	if installer.HeadscaleConfigVolume == "" {
		installer.HeadscaleConfigVolume = "vastora_headscale-config"
	}
	if installer.CaddyDataVolume == "" {
		installer.CaddyDataVolume = "vastora_headscale-caddy-data"
	}
	if installer.CaddyConfigVolume == "" {
		installer.CaddyConfigVolume = "vastora_headscale-caddy-config"
	}
	if installer.HeadscaleImage == "" {
		installer.HeadscaleImage = DefaultHeadscaleImage
	}
	if installer.CaddyImage == "" {
		installer.CaddyImage = DefaultCaddyImage
	}
	if installer.HTTPClient == nil {
		installer.HTTPClient = &http.Client{Timeout: 8 * time.Second}
	}
	return installer, centerURL, headscaleURL, nil
}

func (installer DockerHeadscaleInstaller) replaceHeadscale(ctx context.Context, docker *client.Client) error {
	if err := removeManagedContainer(ctx, docker, DefaultHeadscaleContainer, "center-headscale"); err != nil {
		return err
	}
	config, hostConfig := installer.headscaleContainerConfig()
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: config, HostConfig: hostConfig, Name: DefaultHeadscaleContainer,
	})
	if err != nil {
		return fmt.Errorf("deployer: create Headscale container: %w", err)
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("deployer: start Headscale container: %w", err)
	}
	return nil
}

func (installer DockerHeadscaleInstaller) headscaleContainerConfig() (*container.Config, *container.HostConfig) {
	port := dockernetwork.MustParsePort("8081/tcp")
	return &container.Config{
			Image:        installer.HeadscaleImage,
			Cmd:          []string{"serve"},
			Labels:       map[string]string{"io.vastora.managed": "true", "io.vastora.component": "center-headscale"},
			ExposedPorts: dockernetwork.PortSet{port: struct{}{}},
		}, &container.HostConfig{
			NetworkMode:    container.NetworkMode("bridge"),
			RestartPolicy:  container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")},
			ReadonlyRootfs: true,
			Tmpfs:          map[string]string{"/var/run/headscale": "rw,noexec,nosuid,size=16m,mode=1777"},
			PortBindings: dockernetwork.PortMap{
				port: []dockernetwork.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "8081"}},
			},
			Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: installer.HeadscaleDataVolume, Target: "/var/lib/headscale"},
				{Type: mount.TypeVolume, Source: installer.HeadscaleConfigVolume, Target: "/etc/headscale", ReadOnly: true},
				{Type: mount.TypeVolume, Source: installer.CenterDataVolume, Target: "/var/lib/vastora-shared", ReadOnly: true},
			},
		}
}

func (installer DockerHeadscaleInstaller) replaceGateway(ctx context.Context, docker *client.Client, caddyfile []byte) error {
	if err := removeManagedContainer(ctx, docker, DefaultGatewayContainer, "center-headscale-gateway"); err != nil {
		return err
	}
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:  installer.CaddyImage,
			Cmd:    []string{"caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"},
			Labels: map[string]string{"io.vastora.managed": "true", "io.vastora.component": "center-headscale-gateway"},
		},
		HostConfig: &container.HostConfig{
			NetworkMode:   container.NetworkMode("host"),
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")},
			Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: installer.CaddyDataVolume, Target: "/data"},
				{Type: mount.TypeVolume, Source: installer.CaddyConfigVolume, Target: "/config"},
			},
		},
		Name: DefaultGatewayContainer,
	})
	if err != nil {
		return fmt.Errorf("deployer: create Center gateway: %w", err)
	}
	if err := copyFile(ctx, docker, created.ID, "/etc/caddy", "Caddyfile", caddyfile); err != nil {
		_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
		return err
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("deployer: start Center gateway: %w", err)
	}
	return nil
}

func managedContainerExists(ctx context.Context, docker *client.Client, name, component string) (bool, error) {
	inspected, err := docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("deployer: inspect managed container %s: %w", name, err)
	}
	if inspected.Container.Config == nil {
		return false, fmt.Errorf("deployer: managed container %s has no Docker configuration", name)
	}
	labels := inspected.Container.Config.Labels
	if labels["io.vastora.managed"] != "true" || labels["io.vastora.component"] != component {
		return false, fmt.Errorf("deployer: container name %s is already used by an unmanaged workload", name)
	}
	return true, nil
}

func removeManagedContainer(ctx context.Context, docker *client.Client, name, component string) error {
	exists, err := managedContainerExists(ctx, docker, name, component)
	if err != nil || !exists {
		return err
	}
	if _, err := docker.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("deployer: replace managed container %s: %w", name, err)
	}
	return nil
}

func ensurePortsAvailable(ports ...int) error {
	listeners := make([]net.Listener, 0, len(ports))
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	for _, port := range ports {
		listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			return fmt.Errorf("deployer: TCP port %d is unavailable; stop the conflicting service before installing built-in Headscale", port)
		}
		listeners = append(listeners, listener)
	}
	return nil
}

func createHeadscaleAPIKey(ctx context.Context, docker *client.Client, containerName string) (string, error) {
	exec, err := docker.ExecCreate(ctx, containerName, client.ExecCreateOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          []string{"headscale", "apikeys", "create", "--expiration", "365d"},
	})
	if err != nil {
		return "", fmt.Errorf("deployer: create Headscale API key command: %w", err)
	}
	attached, err := docker.ExecAttach(ctx, exec.ID, client.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("deployer: run Headscale API key command: %w", err)
	}
	defer attached.Close()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attached.Reader); err != nil {
		return "", fmt.Errorf("deployer: read Headscale API key command: %w", err)
	}
	status, err := docker.ExecInspect(ctx, exec.ID, client.ExecInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("deployer: inspect Headscale API key command: %w", err)
	}
	if status.ExitCode != 0 {
		return "", fmt.Errorf("deployer: Headscale API key command failed: %s", strings.TrimSpace(stderr.String()))
	}
	key := strings.TrimSpace(stdout.String())
	if len(key) < 20 || strings.ContainsAny(key, "\r\n \t") {
		return "", errors.New("deployer: Headscale returned an invalid API key")
	}
	return key, nil
}

func copyFile(ctx context.Context, docker *client.Client, containerID, destination, name string, content []byte) error {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), ModTime: time.Unix(0, 0)}); err != nil {
		return err
	}
	if _, err := writer.Write(content); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if _, err := docker.CopyToContainer(ctx, containerID, client.CopyToContainerOptions{DestinationPath: destination, Content: bytes.NewReader(archive.Bytes())}); err != nil {
		return fmt.Errorf("deployer: install gateway configuration: %w", err)
	}
	return nil
}

func writeAtomic(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("deployer: create configuration directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".vastora-config-*")
	if err != nil {
		return fmt.Errorf("deployer: create temporary configuration: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("deployer: publish configuration: %w", err)
	}
	return nil
}

func waitForURL(ctx context.Context, httpClient *http.Client, target string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return err
		}
		response, err := httpClient.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("HTTP %d", response.StatusCode)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if lastErr == nil {
		lastErr = errors.New("timeout")
	}
	return lastErr
}

func waitForLocalGateway(ctx context.Context, endpoint, healthPath string, timeout time.Duration) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:443")
		},
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: parsed.Hostname()},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://127.0.0.1"+healthPath, nil)
		if err != nil {
			return err
		}
		request.Host = parsed.Host
		response, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("HTTP %d", response.StatusCode)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if lastErr == nil {
		lastErr = errors.New("timeout")
	}
	return lastErr
}
