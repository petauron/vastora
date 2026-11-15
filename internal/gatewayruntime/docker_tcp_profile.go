package gatewayruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func ReadContainerTCPProfile(ctx context.Context, docker *client.Client, id string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	execution, err := docker.ExecCreate(ctx, id, client.ExecCreateOptions{
		AttachStdout: true, AttachStderr: true, Cmd: append([]string{"/bin/cat"}, tcpProfilePaths...),
	})
	if err != nil {
		return nil, fmt.Errorf("gateway: create TCP profile readback: %w", err)
	}
	attached, err := docker.ExecAttach(ctx, execution.ID, client.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("gateway: attach TCP profile readback: %w", err)
	}
	defer attached.Close()
	stop := context.AfterFunc(ctx, attached.Close)
	defer stop()
	profile, readErr := decodeTCPProfile(attached.Reader)
	status, err := docker.ExecInspect(ctx, execution.ID, client.ExecInspectOptions{})
	if err != nil {
		return nil, err
	}
	if status.Running || status.ExitCode != 0 {
		return nil, errors.New("gateway: TCP profile readback did not complete successfully")
	}
	return profile, readErr
}

// The Deployer itself is bridged. A short-lived, read-only process in the
// Docker host's network namespace reads the host values; reading its own /proc
// would silently reproduce the very drift this profile is meant to prevent.
// Reuse the pinned Caddy image, with no listener, mounts or capabilities.
func DockerHostTCPProfile(ctx context.Context, docker *client.Client, image string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: image, User: "65534:65534", Entrypoint: []string{"/bin/cat"}, Cmd: tcpProfilePaths,
			Labels: map[string]string{ManagedLabel: "true", ComponentLabel: "gateway-tcp-profile-reader"},
		},
		HostConfig: &container.HostConfig{
			NetworkMode: "host", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("gateway: create host TCP profile reader: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = docker.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	wait := docker.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return nil, fmt.Errorf("gateway: start host TCP profile reader: %w", err)
	}
	select {
	case err := <-wait.Error:
		return nil, fmt.Errorf("gateway: wait for host TCP profile reader: %w", err)
	case result := <-wait.Result:
		if result.StatusCode != 0 {
			return nil, errors.New("gateway: host TCP profile reader failed")
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	logs, err := docker.ContainerLogs(ctx, created.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return nil, err
	}
	defer logs.Close()
	return decodeTCPProfile(logs)
}

func decodeTCPProfile(reader io.Reader) (map[string]string, error) {
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, io.LimitReader(reader, 4096)); err != nil {
		return nil, err
	}
	if stderr.Len() != 0 || stdout.Len() > 1024 {
		return nil, errors.New("gateway: invalid TCP profile readback")
	}
	return parseTCPProfile(stdout.String())
}
