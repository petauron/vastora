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
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

const (
	defaultCaddyImage       = "docker.io/library/caddy:2.11.4@sha256:df7f1c2fb114453b951de51a98efc010db1655a92c2e86be6706714e2417a78d"
	defaultCaddyContainer   = "vastora-gateway-caddy"
	defaultCaddyAdminSocket = "/run/vastora/caddy-admin.sock"
)

type GatewayProvisioner interface {
	Ensure(context.Context) error
	Remove(context.Context) error
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

func (provisioner DockerGatewayProvisioner) Ensure(ctx context.Context) error {
	docker, err := provisioner.client()
	if err != nil {
		return err
	}
	defer docker.Close()
	settings, err := provisioner.settings()
	if err != nil {
		return err
	}
	pull, err := docker.ImagePull(ctx, settings.Image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("agent: pull Caddy image: %w", err)
	}
	_, _ = io.Copy(io.Discard, pull)
	_ = pull.Close()
	for _, name := range []string{settings.DataVolume, settings.ConfigVolume} {
		if _, err := docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name}); err != nil {
			return fmt.Errorf("agent: create Caddy volume %s: %w", name, err)
		}
	}
	if _, err := docker.ContainerRemove(ctx, settings.Container, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
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
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:  settings.Image,
			Cmd:    []string{"caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"},
			Labels: map[string]string{"io.vastora.managed": "true", "io.vastora.component": "gateway"},
		},
		HostConfig: &container.HostConfig{
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")},
			NetworkMode:   container.NetworkMode("host"),
			Mounts:        mounts,
		},
		Name: settings.Container,
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
	if _, err := docker.ContainerRemove(ctx, settings.Container, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("agent: remove Caddy container: %w", err)
	}
	return nil
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
