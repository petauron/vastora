package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
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

// pulseServiceCLI is only used by fixed Pulse administration operations. Neither an
// HTTP caller nor a catalog may supply an executable or arbitrary arguments.
func pulseServiceCLI(ctx context.Context, docker *client.Client, containerID string, args []string, input io.Reader) ([]byte, error) {
	timeout := 30 * time.Second
	if len(args) == 1 && args[0] == "backup" {
		// Copying and checking the metrics database can exceed the short
		// administration deadline. Keep a bounded backup budget and still
		// require successful completion before replacing the service.
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	execution, err := docker.ExecCreate(ctx, containerID, client.ExecCreateOptions{Cmd: append([]string{"/usr/local/bin/pulse-service"}, args...), AttachStdout: true, AttachStderr: true, AttachStdin: input != nil})
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
	if input != nil {
		if _, err := io.Copy(attached.Conn, input); err != nil {
			return nil, errors.New("agent: Pulse administration input failed")
		}
		if err := attached.CloseWrite(); err != nil {
			return nil, errors.New("agent: Pulse administration input failed")
		}
	}
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

func (e ApplicationExecutor) InspectPulse(ctx context.Context, task pulse.InspectionTask) (pulse.InspectionResult, error) {
	result := pulse.InspectionResult{}
	if err := task.Validate(); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return result, err
	}
	defer docker.Close()
	containerID, err := e.pulseServiceContainerID(ctx, docker, task.ApplicationID, task.DeploymentID)
	if err != nil {
		return result, err
	}
	return inspectPulseEnrollments(ctx, docker, containerID, task)
}

func inspectPulseEnrollments(ctx context.Context, docker *client.Client, containerID string, task pulse.InspectionTask) (pulse.InspectionResult, error) {
	result := pulse.InspectionResult{}
	help, err := pulseServiceCLI(ctx, docker, containerID, []string{"--help"}, nil)
	if err != nil {
		return result, err
	}
	if !strings.Contains(string(help), "enrollment inspect ID") {
		return result, errors.New("agent: installed Pulse does not support enrollment inspection; upgrade the managed service explicitly")
	}
	for _, id := range task.EnrollmentIDs {
		output, err := pulseServiceCLI(ctx, docker, containerID, []string{"enrollment", "inspect", id}, nil)
		if err != nil {
			return pulse.InspectionResult{}, err
		}
		var record pulse.EnrollmentRecord
		decoder := json.NewDecoder(bytes.NewReader(output))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&record) != nil || record.ID != id {
			return pulse.InspectionResult{}, errors.New("agent: invalid Pulse inspection response")
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			return pulse.InspectionResult{}, errors.New("agent: invalid Pulse inspection response")
		}
		result.Records = append(result.Records, record)
	}
	if err := result.Validate(task); err != nil {
		return pulse.InspectionResult{}, err
	}
	return result, nil
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
	output, err := pulseServiceCLI(ctx, docker, resource.ID, []string{"enrollment", "create", "--ttl-seconds", "3600"}, nil)
	if err != nil {
		return pulse.EnrollmentResult{}, err
	}
	var result pulse.EnrollmentResult
	if json.Unmarshal(output, &result) != nil || result.ID == "" || len(result.Token) < 16 || len(result.Token) > 512 || result.ExpiresAtUnixMS <= time.Now().UnixMilli() {
		return pulse.EnrollmentResult{}, errors.New("agent: invalid Pulse enrollment response")
	}
	return result, nil
}

// A failed invocation may already have replaced the credential. The execution
// journal retains an uncertain outcome; it must never be retried automatically.
func (e ApplicationExecutor) RotatePulse(ctx context.Context, task pulse.RotationTask) (pulse.RotationResult, error) {
	if err := task.Validate(); err != nil {
		return pulse.RotationResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return pulse.RotationResult{}, err
	}
	defer docker.Close()
	containerID, err := e.pulseServiceContainerID(ctx, docker, task.Inspection.ApplicationID, task.Inspection.DeploymentID)
	if err != nil {
		return pulse.RotationResult{}, errors.New("agent: reviewed Pulse service is unavailable")
	}
	// Pin every read and the write to this container ID, even if the managed name
	// is reassigned while the operation is running.
	records, err := inspectPulseEnrollments(ctx, docker, containerID, task.Inspection)
	if err != nil {
		return pulse.RotationResult{}, err
	}
	id, err := records.OriginalNodeID(task.Inspection)
	if err != nil || id != task.NodeID {
		return pulse.RotationResult{}, errors.New("agent: original Pulse identity changed before credential rotation")
	}
	output, err := pulseServiceCLI(ctx, docker, containerID, []string{"node", "rotate", task.NodeID}, nil)
	if err != nil {
		return pulse.RotationResult{}, uncertainTaskOutcome(errors.New("agent: Pulse credential rotation outcome needs inspection"))
	}
	result := pulse.RotationResult{NodeID: task.NodeID, Token: strings.TrimSpace(string(output))}
	if result.Validate(task) != nil {
		return pulse.RotationResult{}, uncertainTaskOutcome(errors.New("agent: Pulse credential rotation response needs inspection"))
	}
	return result, nil
}

func (e ApplicationExecutor) InspectPulseReporting(ctx context.Context, task pulse.ReportingTask) (pulse.ReportingResult, error) {
	var result pulse.ReportingResult
	if err := task.Validate(); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return result, err
	}
	defer docker.Close()
	containerID, err := e.pulseServiceContainerID(ctx, docker, task.ApplicationID, task.DeploymentID)
	if err != nil {
		return result, errors.New("agent: reviewed Pulse service is unavailable")
	}
	help, err := pulseServiceCLI(ctx, docker, containerID, []string{"--help"}, nil)
	if err != nil || !strings.Contains(string(help), "node reporting ID") {
		return result, errors.New("agent: Pulse service does not support reporting inspection")
	}
	output, err := pulseServiceCLI(ctx, docker, containerID, []string{"node", "reporting", task.Credentials.NodeID}, strings.NewReader(task.Credentials.Token+"\n"))
	if err != nil {
		return result, errors.New("agent: Pulse reporting inspection failed")
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || result.Validate(task) != nil {
		return pulse.ReportingResult{}, errors.New("agent: invalid Pulse reporting response")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return pulse.ReportingResult{}, errors.New("agent: invalid Pulse reporting response")
	}
	return result, nil
}

// Resolve the reviewed service through its package receipt, including adopted names.
func (e ApplicationExecutor) pulseServiceContainerID(ctx context.Context, docker *client.Client, applicationID, deploymentID string) (string, error) {
	receipt, err := e.ApplicationResources(applicationID)
	if err != nil || receipt.AppKey != pulse.ServiceKey || receipt.State != "ready" {
		return "", errors.New("agent: Pulse package ownership receipt unavailable")
	}
	resource := resourceNamed(receipt, "container", "pulse")
	if resource == nil {
		return "", errors.New("agent: Pulse container receipt unavailable")
	}
	current, exists, err := inspectOwnedApplicationContainer(ctx, docker, resource.Name, pulse.ServiceKey, resource.Component, applicationID, deploymentID)
	if err != nil {
		return "", err
	}
	if !exists || current.Container.ID != resource.ID {
		return "", errors.New("agent: reviewed Pulse service is unavailable")
	}
	return resource.ID, nil
}
