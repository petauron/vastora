package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/pulse"
)

const pulseContainer = "vastora-pulse"

func deployPulse(ctx context.Context, docker *client.Client, task DeploymentTask, bindAddress string) error {
	image, err := pullDeclaredImage(ctx, docker, task, "pulse")
	if err != nil {
		return err
	}
	if err := ensureOwnedApplicationVolumes(ctx, docker, applicationVolumes[pulse.ServiceKey], pulse.ServiceKey, task.ApplicationID); err != nil {
		return err
	}
	_, exists, err := inspectOwnedApplicationContainer(ctx, docker, pulseContainer, pulse.ServiceKey, "pulse", task.ApplicationID, anyApplicationDeployment)
	if err != nil {
		return err
	}
	if exists && task.Operation == "upgrade" {
		// Pulse performs forward-only migrations at startup. Take its supported
		// online SQLite backup before replacing the running service; never start
		// an old image against a database touched by the new one.
		if _, err := pulseServiceCLI(ctx, docker, []string{"backup"}); err != nil {
			return errors.New("agent: Pulse backup failed; upgrade was not started")
		}
	}
	if err := removeOwnedApplicationContainer(ctx, docker, pulseContainer, pulse.ServiceKey, "pulse", task.ApplicationID, anyApplicationDeployment); err != nil {
		return err
	}
	port := network.MustParsePort("8080/tcp")
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:   pulseContainer,
		Config: &container.Config{Image: image, User: "65532:65532", Labels: applicationResourceLabels(pulse.ServiceKey, "pulse", task.ApplicationID, task.ID), ExposedPorts: network.PortSet{port: struct{}{}}},
		HostConfig: &container.HostConfig{
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")},
			PortBindings:  network.PortMap{port: []network.PortBinding{{HostIP: netip.MustParseAddr(bindAddress), HostPort: "18080"}}},
			Mounts:        []mount.Mount{{Type: mount.TypeVolume, Source: "vastora-pulse-data", Target: "/var/lib/pulse"}},
			SecurityOpt:   []string{"no-new-privileges:true"},
			CapDrop:       []string{"ALL"},
			LogConfig:     container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3"}},
		},
	})
	if err != nil {
		return err
	}
	_, err = docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{})
	return err
}

// pulseServiceCLI is only used by the two fixed operations below. Neither an
// HTTP caller nor a catalog may supply an executable or arbitrary arguments.
func pulseServiceCLI(ctx context.Context, docker *client.Client, args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	execution, err := docker.ExecCreate(ctx, pulseContainer, client.ExecCreateOptions{Cmd: append([]string{"/usr/local/bin/pulse-service"}, args...), AttachStdout: true, AttachStderr: true})
	if err != nil {
		return nil, errors.New("agent: Pulse administration could not start")
	}
	attached, err := docker.ExecAttach(ctx, execution.ID, client.ExecAttachOptions{})
	if err != nil {
		return nil, errors.New("agent: Pulse administration connection failed")
	}
	defer attached.Close()
	stopClose := context.AfterFunc(ctx, func() { attached.Close() })
	defer stopClose()
	var stdout bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, io.Discard, io.LimitReader(attached.Reader, 64<<10)); err != nil {
		return nil, errors.New("agent: Pulse administration response failed")
	}
	status, err := docker.ExecInspect(ctx, execution.ID, client.ExecInspectOptions{})
	if err != nil || status.Running || status.ExitCode != 0 {
		return nil, errors.New("agent: Pulse administration failed")
	}
	return stdout.Bytes(), nil
}

func (e ApplicationExecutor) EnrollPulse(ctx context.Context, task pulse.EnrollmentTask) (pulse.EnrollmentResult, error) {
	if task.ApplicationID == "" || task.DeploymentID == "" {
		return pulse.EnrollmentResult{}, errors.New("agent: invalid Pulse enrollment task")
	}
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return pulse.EnrollmentResult{}, err
	}
	defer docker.Close()
	_, exists, err := inspectOwnedApplicationContainer(ctx, docker, pulseContainer, pulse.ServiceKey, "pulse", task.ApplicationID, anyApplicationDeployment)
	if err != nil {
		return pulse.EnrollmentResult{}, err
	}
	if !exists {
		return pulse.EnrollmentResult{}, errors.New("agent: managed Pulse service is unavailable")
	}
	output, err := pulseServiceCLI(ctx, docker, []string{"enrollment", "create", "--ttl-seconds", "3600"})
	if err != nil {
		return pulse.EnrollmentResult{}, err
	}
	var result pulse.EnrollmentResult
	if json.Unmarshal(output, &result) != nil || result.ID == "" || len(result.Token) < 16 || len(result.Token) > 512 || result.ExpiresAtUnixMS <= time.Now().UnixMilli() {
		return pulse.EnrollmentResult{}, errors.New("agent: invalid Pulse enrollment response")
	}
	return result, nil
}
