package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/pulse"
)

const (
	pulseContainer          = "vastora-pulse"
	pulseBootstrapDirectory = "pulse-bootstrap"
	pulseSetupTokenPath     = "/" + pulseBootstrapDirectory + "/setup-token"
)

// pulseServiceCLI is only used by the two fixed operations below. Neither an
// HTTP caller nor a catalog may supply an executable or arbitrary arguments.
func pulseServiceCLI(ctx context.Context, docker *client.Client, containerID string, args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	execution, err := docker.ExecCreate(ctx, containerID, client.ExecCreateOptions{Cmd: append([]string{"/usr/local/bin/pulse-service"}, args...), AttachStdout: true, AttachStderr: true})
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
	receipt, err := e.ApplicationResources(task.ApplicationID)
	if err != nil || receipt.AppKey != pulse.ServiceKey || receipt.State != "ready" {
		return pulse.EnrollmentResult{}, errors.New("agent: Pulse package ownership receipt unavailable")
	}
	resource := resourceNamed(receipt, "container", "pulse")
	if resource == nil {
		return pulse.EnrollmentResult{}, errors.New("agent: Pulse container receipt unavailable")
	}
	current, exists, err := inspectOwnedApplicationContainer(ctx, docker, resource.Name, pulse.ServiceKey, resource.Component, task.ApplicationID, anyApplicationDeployment)
	if err != nil {
		return pulse.EnrollmentResult{}, err
	}
	if !exists || current.Container.ID != resource.ID {
		return pulse.EnrollmentResult{}, errors.New("agent: managed Pulse service is unavailable")
	}
	output, err := pulseServiceCLI(ctx, docker, resource.ID, []string{"enrollment", "create", "--ttl-seconds", "3600"})
	if err != nil {
		return pulse.EnrollmentResult{}, err
	}
	var result pulse.EnrollmentResult
	if json.Unmarshal(output, &result) != nil || result.ID == "" || len(result.Token) < 16 || len(result.Token) > 512 || result.ExpiresAtUnixMS <= time.Now().UnixMilli() {
		return pulse.EnrollmentResult{}, errors.New("agent: invalid Pulse enrollment response")
	}
	return result, nil
}
