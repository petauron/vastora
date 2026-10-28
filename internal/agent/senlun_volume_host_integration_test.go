//go:build integration

package agent

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// Exercise Docker's actual archive and mounted-volume semantics on the
// existing isolated GitHub-hosted integration runner.
func TestSenlunVolumeTransferRealDocker(t *testing.T) {
	packageHostedRunner(t)
	image := os.Getenv("VASTORA_LANDING_XRAY_IMAGE")
	if !strings.Contains(image, "@sha256:") {
		t.Fatal("CI must provide its pinned integration image")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	docker, err := client.New(client.WithHost("unix:///var/run/docker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	prefix := packageIdentity(t.Name() + t.TempDir())
	volumes := []string{prefix + "-source", prefix + "-target"}
	var containers []string
	defer func() {
		for _, id := range containers {
			_, _ = docker.ContainerRemove(context.Background(), id, client.ContainerRemoveOptions{})
		}
		for _, name := range volumes {
			_, _ = docker.VolumeRemove(context.Background(), name, client.VolumeRemoveOptions{})
		}
	}()
	for i, name := range volumes {
		if _, err := docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name}); err != nil {
			t.Fatal(err)
		}
		created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{Image: image}, HostConfig: &container.HostConfig{NetworkMode: "none", Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: name, Target: "/var/lib/senlun", VolumeOptions: &mount.VolumeOptions{NoCopy: true}}}}})
		if err != nil {
			t.Fatal(err)
		}
		containers = append(containers, created.ID)
		if i == 0 {
			if _, err := docker.CopyToContainer(ctx, created.ID, client.CopyToContainerOptions{DestinationPath: "/var/lib", Content: bytes.NewReader(senlunTestArchive(t))}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := transferSenlunVolume(ctx, docker, containers[0], containers[1], "/var/lib/senlun"); err != nil {
		t.Fatal(err)
	}
}
