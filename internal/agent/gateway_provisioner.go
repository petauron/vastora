package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/gatewayruntime"
)

const (
	defaultCaddyImage       = gatewayruntime.CaddyImage
	defaultCaddyContainer   = gatewayruntime.CaddyContainer
	defaultCaddyAdminSocket = gatewayruntime.CaddyAdminSocket
)

type GatewayProvisioner interface {
	Ensure(context.Context) error
	Remove(context.Context) error
}

type SystemGatewayInspector interface {
	ProtectedSystemServices(context.Context) ([]string, error)
}

type DockerGatewayProvisioner struct {
	Socket          string
	Image           string
	Container       string
	AdminListen     string
	AdminSocketPath string
	DataVolume      string
	ConfigVolume    string
}

func (provisioner DockerGatewayProvisioner) ProtectedSystemServices(ctx context.Context) ([]string, error) {
	settings, err := provisioner.settings()
	if err != nil {
		return nil, err
	}
	docker, err := provisioner.client()
	if err != nil {
		return nil, err
	}
	defer docker.Close()
	existing, err := inspectManagedCaddy(ctx, docker, settings.Container)
	if err != nil || existing == nil {
		return nil, err
	}
	encoded := strings.TrimSpace(existing.Container.Config.Labels[gatewayruntime.SystemServicesLabel])
	if encoded == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	services := make([]string, 0, 2)
	for _, value := range strings.Split(encoded, ",") {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			services = append(services, value)
		}
	}
	return services, nil
}

func (provisioner DockerGatewayProvisioner) Ensure(ctx context.Context) error {
	return provisioner.reconcile(ctx, nil)
}

// Reconcile publishes exactly the sockets declared by the current desired
// state. Docker port bindings are immutable, so a changed listener set causes
// the managed Caddy container to be replaced before its configuration is
// applied through the shared Admin socket.
func (provisioner DockerGatewayProvisioner) Reconcile(ctx context.Context, desired gateway.DesiredState) error {
	return provisioner.reconcile(ctx, &desired)
}

func (provisioner DockerGatewayProvisioner) reconcile(ctx context.Context, desired *gateway.DesiredState) error {
	settings, err := provisioner.settings()
	if err != nil {
		return err
	}
	docker, err := provisioner.client()
	if err != nil {
		return err
	}
	defer docker.Close()
	if err := dockerruntime.EnsureNetwork(ctx, docker); err != nil {
		return err
	}
	existing, err := inspectManagedCaddy(ctx, docker, settings.Container)
	if err != nil {
		return err
	}
	protectedSystemGateway := existing != nil && strings.TrimSpace(existing.Container.Config.Labels[gatewayruntime.SystemServicesLabel]) != ""
	if protectedSystemGateway && !caddySharesAdminPath(existing.Container.HostConfig, settings.AdminSocketPath) {
		return errors.New("agent: the protected system gateway must be reconciled by the Center deployment helper before Agent can adopt it")
	}
	var exposedPorts dockernetwork.PortSet
	var portBindings dockernetwork.PortMap
	if desired != nil {
		exposedPorts, portBindings, err = gatewayruntime.DockerPorts(*desired)
		if err != nil {
			return err
		}
	}
	matchesRuntime := existing != nil && existing.Container.HostConfig != nil && existing.Container.HostConfig.NetworkMode == container.NetworkMode(dockerruntime.NetworkName)
	matchesPorts := desired == nil || existing != nil && reflect.DeepEqual(existing.Container.Config.ExposedPorts, exposedPorts) && reflect.DeepEqual(existing.Container.HostConfig.PortBindings, portBindings)
	if existing != nil && matchesRuntime && matchesPorts && caddySharesAdminPath(existing.Container.HostConfig, settings.AdminSocketPath) && (protectedSystemGateway || existing.Container.Config.Image == settings.Image) {
		if existing.Container.State != nil && existing.Container.State.Running {
			return nil
		}
		if _, err := docker.ContainerStart(ctx, existing.Container.ID, client.ContainerStartOptions{}); err != nil {
			return fmt.Errorf("agent: start existing Caddy container: %w", err)
		}
		return nil
	}
	pull, err := docker.ImagePull(ctx, settings.Image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("agent: pull Caddy image: %w", err)
	}
	_, _ = io.Copy(io.Discard, pull)
	_ = pull.Close()
	for _, name := range []string{settings.DataVolume, settings.ConfigVolume} {
		if _, err := docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Labels: map[string]string{
			gatewayruntime.ManagedLabel:   "true",
			gatewayruntime.ComponentLabel: "gateway-storage",
		}}); err != nil {
			return fmt.Errorf("agent: create Caddy volume %s: %w", name, err)
		}
	}
	if existing != nil {
		if _, err := docker.ContainerRemove(ctx, settings.Container, client.ContainerRemoveOptions{Force: true}); err != nil {
			return fmt.Errorf("agent: replace Caddy container: %w", err)
		}
	} else if _, err := docker.ContainerRemove(ctx, settings.Container, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("agent: replace Caddy container: %w", err)
	}
	mounts := gatewayMounts(settings)
	if settings.AdminSocketPath != "" {
		adminDirectory := filepath.Dir(settings.AdminSocketPath)
		if err := os.MkdirAll(adminDirectory, 0o700); err != nil {
			return fmt.Errorf("agent: create Caddy Admin socket directory: %w", err)
		}
		if info, err := os.Lstat(settings.AdminSocketPath); err == nil {
			if info.Mode()&os.ModeSocket == 0 {
				return errors.New("agent: Caddy Admin socket path is occupied by a non-socket file")
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("agent: inspect stale Caddy Admin socket: %w", err)
		}
		if err := os.Remove(settings.AdminSocketPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("agent: remove stale Caddy Admin socket: %w", err)
		}
	}
	labels := map[string]string{
		gatewayruntime.ManagedLabel:   "true",
		gatewayruntime.ComponentLabel: gatewayruntime.CaddyComponentLabel,
	}
	if protectedSystemGateway {
		labels[gatewayruntime.SystemServicesLabel] = existing.Container.Config.Labels[gatewayruntime.SystemServicesLabel]
	}
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:        settings.Image,
			Cmd:          []string{"caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"},
			ExposedPorts: exposedPorts,
			Labels:       labels,
		},
		HostConfig: &container.HostConfig{
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")},
			NetworkMode:   container.NetworkMode(dockerruntime.NetworkName),
			PortBindings:  portBindings,
			Mounts:        mounts,
		},
		NetworkingConfig: dockerruntime.NetworkingConfig(dockerruntime.CaddyAlias),
		Name:             settings.Container,
	})
	if err != nil {
		return fmt.Errorf("agent: create Caddy container: %w", err)
	}
	if err := copyCaddyfile(ctx, docker, created.ID, settings.AdminListen); err != nil {
		_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
		return err
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("agent: start Caddy container: %w", err)
	}
	return nil
}

func gatewayMounts(settings DockerGatewayProvisioner) []mount.Mount {
	mounts := []mount.Mount{
		{Type: mount.TypeVolume, Source: settings.DataVolume, Target: "/data"},
		{Type: mount.TypeVolume, Source: settings.ConfigVolume, Target: "/config"},
	}
	if settings.AdminSocketPath != "" {
		adminDirectory := filepath.Dir(settings.AdminSocketPath)
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: adminDirectory, Target: adminDirectory})
	}
	return mounts
}

func (provisioner DockerGatewayProvisioner) Remove(ctx context.Context) error {
	docker, err := provisioner.client()
	if err != nil {
		return err
	}
	defer docker.Close()
	settings, err := provisioner.settings()
	if err != nil {
		return err
	}
	existing, err := inspectManagedCaddy(ctx, docker, settings.Container)
	if err != nil || existing == nil {
		return err
	}
	if strings.TrimSpace(existing.Container.Config.Labels[gatewayruntime.SystemServicesLabel]) != "" {
		return errors.New("agent: refusing to remove Caddy while it serves Center system routes")
	}
	if _, err := docker.ContainerRemove(ctx, settings.Container, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("agent: remove Caddy container: %w", err)
	}
	return nil
}

func inspectManagedCaddy(ctx context.Context, docker *client.Client, name string) (*client.ContainerInspectResult, error) {
	inspection, err := docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("agent: inspect Caddy container: %w", err)
	}
	if inspection.Container.Config == nil || inspection.Container.Config.Labels[gatewayruntime.ManagedLabel] != "true" || inspection.Container.Config.Labels[gatewayruntime.ComponentLabel] != gatewayruntime.CaddyComponentLabel {
		return nil, fmt.Errorf("agent: container name %s is already used by an unmanaged workload", name)
	}
	return &inspection, nil
}

func caddySharesAdminPath(host *container.HostConfig, socketPath string) bool {
	if socketPath == "" {
		return true
	}
	if host == nil {
		return false
	}
	directory := filepath.Dir(socketPath)
	for _, value := range host.Mounts {
		if value.Type == mount.TypeBind && value.Source == directory && value.Target == directory {
			return true
		}
	}
	return false
}

func (provisioner DockerGatewayProvisioner) client() (*client.Client, error) {
	socket := provisioner.Socket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return nil, fmt.Errorf("agent: connect Docker for gateway provisioning: %w", err)
	}
	return docker, nil
}

func (provisioner DockerGatewayProvisioner) settings() (DockerGatewayProvisioner, error) {
	if provisioner.Image == "" {
		provisioner.Image = defaultCaddyImage
	}
	if provisioner.Container == "" {
		provisioner.Container = defaultCaddyContainer
	}
	if provisioner.AdminListen == "" && provisioner.AdminSocketPath == "" {
		provisioner.AdminSocketPath = defaultCaddyAdminSocket
		provisioner.AdminListen = "unix/" + defaultCaddyAdminSocket
	}
	if provisioner.AdminListen == "" {
		provisioner.AdminListen = "unix/" + provisioner.AdminSocketPath
	}
	if provisioner.AdminSocketPath != "" && !filepath.IsAbs(provisioner.AdminSocketPath) {
		return DockerGatewayProvisioner{}, fmt.Errorf("agent: Caddy Admin socket path must be absolute")
	}
	if provisioner.DataVolume == "" {
		provisioner.DataVolume = "vastora-gateway-caddy-data"
	}
	if provisioner.ConfigVolume == "" {
		provisioner.ConfigVolume = "vastora-gateway-caddy-config"
	}
	return provisioner, nil
}

func copyCaddyfile(ctx context.Context, docker *client.Client, containerID, adminListen string) error {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	adminDirective := adminListen
	if strings.HasPrefix(adminListen, "unix/") {
		adminDirective += "|0600"
	}
	content := []byte(fmt.Sprintf("{\n\tadmin %s\n\tpersist_config off\n}\n", adminDirective))
	if err := writer.WriteHeader(&tar.Header{Name: "Caddyfile", Mode: 0o600, Size: int64(len(content)), ModTime: time.Unix(0, 0)}); err != nil {
		return err
	}
	if _, err := writer.Write(content); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if _, err := docker.CopyToContainer(ctx, containerID, client.CopyToContainerOptions{DestinationPath: "/etc/caddy", Content: bytes.NewReader(archive.Bytes())}); err != nil {
		return fmt.Errorf("agent: install Caddy bootstrap configuration: %w", err)
	}
	return nil
}
